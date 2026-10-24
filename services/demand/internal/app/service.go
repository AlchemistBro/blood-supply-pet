package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/demand"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/domain"
)

// Действия аудита demand (NFR008). В details нет содержимого строк и имени файла: имя задаёт
// пользователь, и в нём может оказаться что угодно.
const (
	ActionFileUploaded = "FILE_UPLOADED"
	ActionFileRejected = "FILE_REJECTED"
	objectTypeFile     = "statistics_file"
)

// MaxFileNameLen — statistics_file.file_name varchar(256).
const MaxFileNameLen = 256

// errDuplicate — внутренний сигнал отката транзакции при повторе файла.
var errDuplicate = errors.New("повтор файла")

// Service — сценарии demand.
type Service struct {
	pool    *pgxpool.Pool
	store   Store
	journal *messaging.Journal
	clock   clock.Clock
	ref     *domain.Reference
	log     *slog.Logger
}

// New собирает сервис.
func New(pool *pgxpool.Pool, store Store, journal *messaging.Journal, c clock.Clock, ref *domain.Reference, log *slog.Logger) *Service {
	return &Service{pool: pool, store: store, journal: journal, clock: c, ref: ref, log: log}
}

// parsed — результат разбора файла любого вида.
type parsed struct {
	check    domain.Check
	profiles []domain.DistrictProfile
	history  *domain.ConsumptionHistoryFile
	shares   *domain.ClinicShareFile
	// replace — записи истории прежних файлов, которые заменяет этот файл.
	replace []uuid.UUID
}

func (s *Service) parse(kind domain.FileKind, content []byte) (parsed, error) {
	switch kind {
	case domain.FileKindDistrictProfile:
		f := domain.ParseDistrictProfiles(content, s.ref)
		return parsed{check: f.Check, profiles: f.Profiles}, nil
	case domain.FileKindConsumptionHistory:
		f := domain.ParseConsumptionHistory(content, s.ref)
		return parsed{check: f.Check, history: &f}, nil
	case domain.FileKindClinicShare:
		f := domain.ParseClinicShares(content, s.ref)
		return parsed{check: f.Check, shares: &f}, nil
	default:
		return parsed{}, fmt.Errorf("%w: неизвестный вид файла %q", ErrInvalidUpload, kind)
	}
}

// Upload проверяет файл и сохраняет его одной транзакцией: statistics_file, ошибки строк, принятые строки
// (для долей — новую версию O16) и запись аудита (FR016, FR017, FR045, FR046, NFR008). Файл, отклонённый
// целиком, тоже сохраняется без строк: аналитик видит его в истории загрузок, повтор того же содержимого
// отклоняется по SHA-256.
func (s *Service) Upload(ctx context.Context, in UploadInput) (UploadResult, error) {
	if _, err := domain.ParseFileKind(string(in.Kind)); err != nil {
		return UploadResult{}, fmt.Errorf("%w: %w", ErrInvalidUpload, err)
	}
	p, err := s.parse(in.Kind, in.Content)
	if err != nil {
		return UploadResult{}, err
	}
	digest := sha256.Sum256(in.Content)
	checksum := hex.EncodeToString(digest[:])
	id, err := uuid.NewV7()
	if err != nil {
		return UploadResult{}, fmt.Errorf("идентификатор файла: %w", err)
	}
	var res UploadResult
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.store.LockUploads(ctx, tx, in.Kind); err != nil {
			return err
		}
		if p.history != nil {
			replace, err := s.reconcileHistory(ctx, tx, p.history)
			if err != nil {
				return err
			}
			p.check, p.replace = p.history.Check, replace
		}
		check := p.check
		info := FileInfo{
			ID: id, Kind: in.Kind, FileName: cleanFileName(in.FileName), Checksum: checksum,
			DataSource: check.DataSource, Period: check.Period, Status: check.Status,
			RejectedRows: check.RejectedRows(), AcceptedRows: check.AcceptedRows,
			UploadedBy: in.Actor.UserID, UploadedAt: s.clock.Now(),
		}
		reasons := check.RowReasons()
		if check.FileError != "" {
			// Ошибка всего файла хранится строкой 1 (заголовок): отдельного поля в ПР6 нет.
			reasons = []domain.RowReason{{Row: 1, Reason: check.FileError}}
			info.RejectedRows = 1
		}
		inserted, err := s.store.InsertFile(ctx, tx, info)
		if err != nil {
			return err
		}
		if !inserted {
			return errDuplicate
		}
		if err := s.store.InsertRowErrors(ctx, tx, id, reasons); err != nil {
			return err
		}
		res = UploadResult{File: info, FileError: check.FileError, RowErrors: reasons}
		if check.Status != domain.StatusRejected {
			versionNo, err := s.storeRows(ctx, tx, id, p)
			if err != nil {
				return err
			}
			res.ShareVersionNo = versionNo
		}
		action := ActionFileUploaded
		if check.Status == domain.StatusRejected {
			action = ActionFileRejected
		}
		details := map[string]any{
			"file_kind": string(in.Kind), "check_status": string(check.Status),
			"accepted_rows": info.AcceptedRows, "rejected_rows": info.RejectedRows,
		}
		if res.ShareVersionNo > 0 {
			details["clinic_share_version_no"] = res.ShareVersionNo
		}
		if len(p.replace) > 0 && check.Status != domain.StatusRejected {
			details["replaced_history_rows"] = len(p.replace)
		}
		var payload contracts.Payload = demand.StatisticsFileAccepted{StatisticsFileID: id, FileKind: string(in.Kind), Status: string(check.Status), AcceptedRows: &info.AcceptedRows, RejectedRows: info.RejectedRows, UploadedBy: in.Actor.UserID}
		if check.Status == domain.StatusRejected {
			reason := demand.RejectInvalidRows
			if check.FileError != "" {
				reason = demand.RejectInvalidFormat
			}
			payload = demand.StatisticsFileRejected{StatisticsFileID: id, FileKind: string(in.Kind), Reason: reason, RejectedRows: info.RejectedRows, UploadedBy: in.Actor.UserID}
		}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicDemandEvents, id.String(), payload); err != nil {
			return err
		}
		if res.ShareVersionNo > 0 {
			event, err := s.store.SharesEvent(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := s.journal.Publish(ctx, tx, contracts.TopicDemandEvents, event.ClinicShareVersionID.String(), event); err != nil {
				return err
			}
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{
			UserID: in.Actor.UserID, Role: in.Actor.Role, Action: action, ObjectType: objectTypeFile,
			ObjectID: id.String(), Result: auditc.ResultSuccess, Details: details,
		})
	})
	if errors.Is(err, errDuplicate) {
		return UploadResult{}, s.duplicate(ctx, in, checksum)
	}
	if err != nil {
		return UploadResult{}, fmt.Errorf("загрузка файла: %w", err)
	}
	s.log.InfoContext(ctx, "файл аналитика проверен", "statistics_file_id", id, "file_kind", in.Kind,
		"check_status", res.File.Status, "accepted_rows", res.File.AcceptedRows, "rejected_rows", res.File.RejectedRows)
	return res, nil
}

// reconcileHistory сверяет строки истории с уже загруженной из файлов историей того же вида (FR019, FR020):
//   - тот же вид и точно тот же период, прежняя запись не вошла в оценку — замена (исправленный файл);
//   - тот же период, но прежняя запись вошла в версию оценки — отказ: оценка должна воспроизводиться;
//   - частичное пересечение — отказ: иначе расход вошёл бы в базу B(k, p) дважды.
//
// Возвращает идентификаторы заменяемых записей.
func (s *Service) reconcileHistory(ctx context.Context, tx pgx.Tx, f *domain.ConsumptionHistoryFile) ([]uuid.UUID, error) {
	stored, err := s.store.HistoryPeriods(ctx, tx, domain.HistorySourceFile)
	if err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		return nil, nil
	}
	byKind := map[string][]StoredPeriod{}
	for _, p := range stored {
		byKind[p.KindCode] = append(byKind[p.KindCode], p)
	}
	var replace []uuid.UUID
	f.RejectRecords(func(r domain.ConsumptionRecord) (string, string, bool) {
		var same *StoredPeriod
		for i := range byKind[r.KindCode] {
			p := &byKind[r.KindCode][i]
			if !p.Period.Overlaps(r.Period) {
				continue
			}
			if !p.Period.Start.Equal(r.Period.Start) || !p.Period.End.Equal(r.Period.End) {
				return "period_start", fmt.Sprintf("период частично пересекается с уже загруженным периодом %s – %s (файл «%s»): "+
					"загрузите историю за те же периоды или за непересекающиеся", formatDate(p.Period.Start), formatDate(p.Period.End), p.FileName), true
			}
			same = p
		}
		if same == nil {
			return "", "", false
		}
		if same.Used {
			return "period_start", fmt.Sprintf("история за %s – %s уже вошла в версию оценки (файл «%s») и не заменяется",
				formatDate(same.Period.Start), formatDate(same.Period.End), same.FileName), true
		}
		replace = append(replace, same.ID)
		return "", "", false
	})
	return replace, nil
}

func formatDate(t time.Time) string { return t.Format("2006-01-02") }

func (s *Service) storeRows(ctx context.Context, tx pgx.Tx, id uuid.UUID, p parsed) (int64, error) {
	switch {
	case p.history != nil:
		return 0, s.store.InsertHistory(ctx, tx, id, p.history.Records, p.replace)
	case p.shares != nil:
		return s.store.InsertShareVersion(ctx, tx, id, p.shares.Basis, p.shares.ValidFrom, p.shares.Shares)
	default:
		return 0, s.store.InsertProfiles(ctx, tx, id, p.profiles)
	}
}

// duplicate фиксирует в аудите отказ в повторной загрузке и возвращает ссылку на ранее загруженный файл.
func (s *Service) duplicate(ctx context.Context, in UploadInput, checksum string) error {
	existing, err := s.store.FileIDByChecksum(ctx, checksum)
	if err != nil {
		return fmt.Errorf("поиск ранее загруженного файла: %w", err)
	}
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{
			UserID: in.Actor.UserID, Role: in.Actor.Role, Action: ActionFileRejected, ObjectType: objectTypeFile,
			ObjectID: existing.String(), Result: auditc.ResultSuccess,
			Details: map[string]any{"file_kind": string(in.Kind), "reason": "DUPLICATE"},
		})
	})
	if err != nil {
		return fmt.Errorf("аудит повторной загрузки: %w", err)
	}
	return &DuplicateError{ExistingID: existing}
}

// cleanFileName оставляет только имя без пути и обрезает его до varchar(256).
func cleanFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || name == "/" || !utf8.ValidString(name) {
		return "file.csv"
	}
	if utf8.RuneCountInString(name) > MaxFileNameLen {
		name = string([]rune(name)[:MaxFileNameLen])
	}
	return name
}

// ListFiles — загруженные файлы, новые первыми.
func (s *Service) ListFiles(ctx context.Context, kind *domain.FileKind, limit int) ([]FileInfo, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.store.ListFiles(ctx, kind, limit)
}

// GetFile — файл с числом принятых строк.
func (s *Service) GetFile(ctx context.Context, id uuid.UUID) (FileInfo, error) {
	return s.store.GetFile(ctx, id)
}

// FileErrors — отклонённые строки файла.
func (s *Service) FileErrors(ctx context.Context, id uuid.UUID) ([]domain.RowReason, error) {
	if _, err := s.store.GetFile(ctx, id); err != nil {
		return nil, err
	}
	return s.store.FileErrors(ctx, id)
}

// History — история расхода (O17).
func (s *Service) History(ctx context.Context, kindCode *string) ([]HistoryRecord, error) {
	return s.store.History(ctx, kindCode)
}

// ShareVersions — версии долей (O16), новые первыми.
func (s *Service) ShareVersions(ctx context.Context) ([]ShareVersion, error) {
	return s.store.ShareVersions(ctx)
}

// CurrentShares — действующая версия долей со строками.
func (s *Service) CurrentShares(ctx context.Context) (ShareVersion, []ShareLine, error) {
	return s.store.CurrentShares(ctx)
}
