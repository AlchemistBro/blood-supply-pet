package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/demand"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/domain"
)

// Ошибки приложения; обработчики HTTP переводят их в коды ответа.
var (
	ErrNotFound      = errors.New("не найдено")
	ErrInvalidUpload = errors.New("неверный запрос на загрузку")
	ErrDuplicateFile = errors.New("этот файл уже загружен")
)

// DuplicateError — повтор файла с тем же SHA-256: указывает на ранее загруженный файл.
type DuplicateError struct {
	ExistingID uuid.UUID
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("этот файл уже загружен (файл %s)", e.ExistingID)
}

// Is позволяет проверять errors.Is(err, ErrDuplicateFile).
func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicateFile }

// Actor — участник из токена.
type Actor struct {
	UserID string
	Role   string
}

// FileInfo — запись statistics_file. Period нулевой, если файл отклонён целиком.
type FileInfo struct {
	ID           uuid.UUID
	Kind         domain.FileKind
	FileName     string
	Checksum     string
	DataSource   string
	Period       domain.Period
	Status       domain.CheckStatus
	RejectedRows int
	AcceptedRows int
	UploadedBy   string
	UploadedAt   time.Time
}

// UploadInput — загружаемый файл.
type UploadInput struct {
	Kind     domain.FileKind
	FileName string
	Content  []byte
	Actor    Actor
}

// UploadResult — итог загрузки для ответа аналитику.
type UploadResult struct {
	File           FileInfo
	FileError      string
	RowErrors      []domain.RowReason
	ShareVersionNo int64
}

// HistoryRecord — запись consumption_history.
type HistoryRecord struct {
	ID               uuid.UUID
	StatisticsFileID *uuid.UUID
	Source           domain.HistorySource
	KindCode         string
	Period           domain.Period
	ConsumedVolumeML int64
}

// StoredPeriod — период уже сохранённой истории, против которого проверяется новый файл.
// Used — запись вошла в версию оценки и не заменяется.
type StoredPeriod struct {
	ID       uuid.UUID
	KindCode string
	Period   domain.Period
	FileName string
	Used     bool
}

// ShareVersion — версия долей район → клиника (O16).
type ShareVersion struct {
	ID               uuid.UUID
	VersionNo        int64
	StatisticsFileID uuid.UUID
	Basis            string
	ValidFrom        time.Time
}

// ShareLine — строка действующей версии долей.
type ShareLine struct {
	DistrictID int64
	ClinicID   int64
	Share      domain.Share
}

// Store — порт хранилища demand. Методы с tx работают внутри транзакции приложения.
type Store interface {
	SharesEvent(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) (demand.ClinicSharesPublished, error)
	LoadEstimateInput(ctx context.Context, tx pgx.Tx, in EstimateSelection) (domain.EstimateInput, error)
	SaveEstimate(ctx context.Context, tx pgx.Tx, record *EstimateRecord, result domain.Estimate) error
	ReadEstimate(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (EstimateRecord, error)
	ListEstimates(ctx context.Context) ([]EstimateRecord, error)
	MarkEstimatePublished(ctx context.Context, tx pgx.Tx, id uuid.UUID, actor string, at time.Time) error
	MarkEstimateRejected(ctx context.Context, tx pgx.Tx, id uuid.UUID, actor, reason string, at time.Time) error
	LockUploads(ctx context.Context, tx pgx.Tx, kind domain.FileKind) error
	HistoryPeriods(ctx context.Context, tx pgx.Tx, source domain.HistorySource) ([]StoredPeriod, error)
	// InsertFile возвращает false, если файл с тем же SHA-256 уже есть.
	InsertFile(ctx context.Context, tx pgx.Tx, f FileInfo) (bool, error)
	InsertRowErrors(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, reasons []domain.RowReason) error
	InsertProfiles(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, profiles []domain.DistrictProfile) error
	// InsertHistory удаляет заменяемые записи replace и сохраняет новые.
	InsertHistory(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, records []domain.ConsumptionRecord, replace []uuid.UUID) error
	// InsertShareVersion создаёт следующую версию долей и возвращает её номер.
	InsertShareVersion(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, basis string, validFrom time.Time, shares []domain.ClinicShare) (int64, error)

	FileIDByChecksum(ctx context.Context, checksum string) (uuid.UUID, error)
	ListFiles(ctx context.Context, kind *domain.FileKind, limit int) ([]FileInfo, error)
	GetFile(ctx context.Context, id uuid.UUID) (FileInfo, error)
	FileErrors(ctx context.Context, id uuid.UUID) ([]domain.RowReason, error)
	History(ctx context.Context, kindCode *string) ([]HistoryRecord, error)
	ShareVersions(ctx context.Context) ([]ShareVersion, error)
	// CurrentShares возвращает ErrNotFound, если версий долей нет.
	CurrentShares(ctx context.Context) (ShareVersion, []ShareLine, error)
}

// ReferenceFrom собирает справочник домена из общего набора libs/refdata (ADR-0013).
func ReferenceFrom(set *refdata.Set) (*domain.Reference, error) {
	districts := make([]domain.DistrictRef, 0, len(set.Districts))
	for _, d := range set.Districts {
		districts = append(districts, domain.DistrictRef{ID: d.DistrictID, Code: d.DistrictCode})
	}
	clinics := make([]domain.ClinicRef, 0, len(set.Clinics))
	for _, c := range set.Clinics {
		clinics = append(clinics, domain.ClinicRef{ID: c.ClinicID, Code: c.ClinicCode})
	}
	kinds := make([]domain.KindRef, 0, len(set.ComponentKinds))
	for _, k := range set.ComponentKinds {
		if k.IsActive {
			kinds = append(kinds, domain.KindRef{Code: k.Code, IsGroupApplicable: k.IsGroupApplicable})
		}
	}
	groups := make([]string, 0, len(set.BloodGroups))
	for _, g := range set.BloodGroups {
		groups = append(groups, g.Code)
	}
	ref, err := domain.NewReference(districts, clinics, kinds, groups)
	if err != nil {
		return nil, fmt.Errorf("справочник demand: %w", err)
	}
	return ref, nil
}
