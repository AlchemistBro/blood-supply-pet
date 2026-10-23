// Package postgres — хранилище demand на sqlc-запросах (база demand).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/domain"
)

// Store реализует app.Store.
type Store struct {
	pool *pgxpool.Pool
}

var _ app.Store = (*Store)(nil)

// New создаёт хранилище.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// LockUploads сериализует загрузки одного вида файла до конца транзакции (advisory lock).
func (s *Store) LockUploads(ctx context.Context, tx pgx.Tx, kind domain.FileKind) error {
	if err := gen.New(tx).LockUploads(ctx, "demand.upload."+string(kind)); err != nil {
		return fmt.Errorf("блокировка загрузок %s: %w", kind, err)
	}
	return nil
}

// HistoryPeriods — периоды сохранённой истории указанного источника.
func (s *Store) HistoryPeriods(ctx context.Context, tx pgx.Tx, source domain.HistorySource) ([]app.StoredPeriod, error) {
	rows, err := gen.New(tx).HistoryPeriods(ctx, string(source))
	if err != nil {
		return nil, fmt.Errorf("периоды истории: %w", err)
	}
	out := make([]app.StoredPeriod, 0, len(rows))
	for _, r := range rows {
		p := app.StoredPeriod{ID: r.ConsumptionHistoryID, KindCode: r.ComponentKindCode,
			Period: fromDates(r.PeriodStart, r.PeriodEnd), Used: r.Used}
		if r.FileName != nil {
			p.FileName = *r.FileName
		}
		out = append(out, p)
	}
	return out, nil
}

// InsertFile вставляет statistics_file; false — файл с тем же SHA-256 уже есть (ON CONFLICT DO NOTHING).
func (s *Store) InsertFile(ctx context.Context, tx pgx.Tx, f app.FileInfo) (bool, error) {
	_, err := gen.New(tx).InsertStatisticsFile(ctx, gen.InsertStatisticsFileParams{
		StatisticsFileID: f.ID, FileKind: string(f.Kind), FileName: f.FileName, FileChecksum: f.Checksum,
		DataSource: nullable(f.DataSource), PeriodStart: toDate(f.Period.Start), PeriodEnd: toDate(f.Period.End),
		CheckStatus: string(f.Status), RejectedRows: clampInt32(f.RejectedRows), UploadedBy: f.UploadedBy,
		UploadedAt: pgtype.Timestamptz{Time: f.UploadedAt, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("запись файла: %w", err)
	}
	return true, nil
}

// InsertRowErrors сохраняет причины отклонения строк.
func (s *Store) InsertRowErrors(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, reasons []domain.RowReason) error {
	if len(reasons) == 0 {
		return nil
	}
	params := make([]gen.InsertFileRowErrorsParams, 0, len(reasons))
	for _, r := range reasons {
		params = append(params, gen.InsertFileRowErrorsParams{StatisticsFileID: fileID, RowNo: clampInt32(r.Row), ErrorReason: r.Reason})
	}
	if _, err := gen.New(tx).InsertFileRowErrors(ctx, params); err != nil {
		return fmt.Errorf("запись ошибок строк: %w", err)
	}
	return nil
}

// InsertProfiles сохраняет профили районов и доли групп (O04).
func (s *Store) InsertProfiles(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, profiles []domain.DistrictProfile) error {
	if len(profiles) == 0 {
		return nil
	}
	rows := make([]gen.InsertDistrictProfilesParams, 0, len(profiles))
	var shares []gen.InsertDistrictGroupSharesParams
	for _, p := range profiles {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("идентификатор профиля: %w", err)
		}
		if p.Population > math.MaxInt32 {
			return fmt.Errorf("население района %s вне integer", p.DistrictCode)
		}
		rows = append(rows, gen.InsertDistrictProfilesParams{DistrictProfileID: id, StatisticsFileID: fileID,
			DistrictID: p.DistrictID, Population: int32(p.Population)}) //nolint:gosec // диапазон проверен выше
		for g, sh := range p.GroupShares {
			shares = append(shares, gen.InsertDistrictGroupSharesParams{DistrictProfileID: id, BloodGroupCode: g, GroupShare: shareNumeric(sh)})
		}
	}
	q := gen.New(tx)
	if _, err := q.InsertDistrictProfiles(ctx, rows); err != nil {
		return fmt.Errorf("запись профилей районов: %w", err)
	}
	if _, err := q.InsertDistrictGroupShares(ctx, shares); err != nil {
		return fmt.Errorf("запись долей групп: %w", err)
	}
	return nil
}

// InsertHistory сохраняет историю расхода из файла (O17); заменяемые записи прежних файлов удаляются
// той же транзакцией (UNIQUE ПР6 по виду, периоду и источнику не даёт держать обе).
func (s *Store) InsertHistory(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, records []domain.ConsumptionRecord, replace []uuid.UUID) error {
	if len(replace) > 0 {
		if err := gen.New(tx).DeleteConsumptionHistory(ctx, replace); err != nil {
			return fmt.Errorf("замена истории расхода: %w", err)
		}
	}
	if len(records) == 0 {
		return nil
	}
	rows := make([]gen.InsertConsumptionHistoryParams, 0, len(records))
	for _, r := range records {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("идентификатор записи истории: %w", err)
		}
		file := fileID
		rows = append(rows, gen.InsertConsumptionHistoryParams{
			ConsumptionHistoryID: id, StatisticsFileID: &file, HistorySource: string(domain.HistorySourceFile),
			ComponentKindCode: r.KindCode, PeriodStart: toDate(r.Period.Start), PeriodEnd: toDate(r.Period.End),
			ConsumedVolumeMl: r.ConsumedVolumeML,
		})
	}
	if _, err := gen.New(tx).InsertConsumptionHistory(ctx, rows); err != nil {
		return fmt.Errorf("запись истории расхода: %w", err)
	}
	return nil
}

// InsertShareVersion создаёт следующую версию долей (FR046: изменение доли создаёт новую версию).
// Номер версии берётся под блокировкой загрузок CLINIC_SHARE, поэтому гонки за version_no нет.
func (s *Store) InsertShareVersion(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, basis string, validFrom time.Time, shares []domain.ClinicShare) (int64, error) {
	q := gen.New(tx)
	versionNo, err := q.NextClinicShareVersionNo(ctx)
	if err != nil {
		return 0, fmt.Errorf("номер версии долей: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return 0, fmt.Errorf("идентификатор версии долей: %w", err)
	}
	if err := q.InsertClinicShareVersion(ctx, gen.InsertClinicShareVersionParams{
		ClinicShareVersionID: id, VersionNo: versionNo, StatisticsFileID: fileID, Basis: basis, ValidFrom: toDate(validFrom),
	}); err != nil {
		return 0, fmt.Errorf("запись версии долей: %w", err)
	}
	rows := make([]gen.InsertClinicSharesParams, 0, len(shares))
	for _, sh := range shares {
		rows = append(rows, gen.InsertClinicSharesParams{ClinicShareVersionID: id, DistrictID: sh.DistrictID,
			ClinicID: sh.ClinicID, ClinicShare: shareNumeric(sh.Share)})
	}
	if _, err := q.InsertClinicShares(ctx, rows); err != nil {
		return 0, fmt.Errorf("запись долей клиник: %w", err)
	}
	return versionNo, nil
}

// FileIDByChecksum — файл с тем же содержимым.
func (s *Store) FileIDByChecksum(ctx context.Context, checksum string) (uuid.UUID, error) {
	id, err := gen.New(s.pool).FileIDByChecksum(ctx, checksum)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, app.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("поиск файла по SHA-256: %w", err)
	}
	return id, nil
}

// ListFiles — страница файлов.
func (s *Store) ListFiles(ctx context.Context, kind *domain.FileKind, limit int) ([]app.FileInfo, error) {
	var k *string
	if kind != nil {
		v := string(*kind)
		k = &v
	}
	rows, err := gen.New(s.pool).ListFiles(ctx, gen.ListFilesParams{FileKind: k, PageLimit: clampInt32(limit)})
	if err != nil {
		return nil, fmt.Errorf("список файлов: %w", err)
	}
	out := make([]app.FileInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, fileFromRow(r))
	}
	return out, nil
}

// GetFile — файл с числом принятых строк.
func (s *Store) GetFile(ctx context.Context, id uuid.UUID) (app.FileInfo, error) {
	q := gen.New(s.pool)
	row, err := q.GetFile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.FileInfo{}, app.ErrNotFound
	}
	if err != nil {
		return app.FileInfo{}, fmt.Errorf("чтение файла: %w", err)
	}
	f := fileFromRow(row)
	accepted, err := q.CountFileRows(ctx, id)
	if err != nil {
		return app.FileInfo{}, fmt.Errorf("число принятых строк: %w", err)
	}
	f.AcceptedRows = int(accepted)
	return f, nil
}

// FileErrors — причины отклонения строк по порядку.
func (s *Store) FileErrors(ctx context.Context, id uuid.UUID) ([]domain.RowReason, error) {
	rows, err := gen.New(s.pool).ListFileRowErrors(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ошибки строк: %w", err)
	}
	out := make([]domain.RowReason, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.RowReason{Row: int(r.RowNo), Reason: r.ErrorReason})
	}
	return out, nil
}

// History — история расхода, при необходимости по одному виду.
func (s *Store) History(ctx context.Context, kindCode *string) ([]app.HistoryRecord, error) {
	rows, err := gen.New(s.pool).ListConsumptionHistory(ctx, kindCode)
	if err != nil {
		return nil, fmt.Errorf("история расхода: %w", err)
	}
	out := make([]app.HistoryRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, app.HistoryRecord{
			ID: r.ConsumptionHistoryID, StatisticsFileID: r.StatisticsFileID, Source: domain.HistorySource(r.HistorySource),
			KindCode: r.ComponentKindCode, Period: fromDates(r.PeriodStart, r.PeriodEnd), ConsumedVolumeML: r.ConsumedVolumeMl,
		})
	}
	return out, nil
}

// ShareVersions — версии долей, новые первыми.
func (s *Store) ShareVersions(ctx context.Context) ([]app.ShareVersion, error) {
	rows, err := gen.New(s.pool).ListClinicShareVersions(ctx)
	if err != nil {
		return nil, fmt.Errorf("версии долей: %w", err)
	}
	out := make([]app.ShareVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, app.ShareVersion{ID: r.ClinicShareVersionID, VersionNo: r.VersionNo,
			StatisticsFileID: r.StatisticsFileID, Basis: r.Basis, ValidFrom: r.ValidFrom.Time})
	}
	return out, nil
}

// CurrentShares — последняя версия долей со строками.
func (s *Store) CurrentShares(ctx context.Context) (app.ShareVersion, []app.ShareLine, error) {
	rows, err := gen.New(s.pool).CurrentClinicShares(ctx)
	if err != nil {
		return app.ShareVersion{}, nil, fmt.Errorf("действующие доли: %w", err)
	}
	if len(rows) == 0 {
		return app.ShareVersion{}, nil, app.ErrNotFound
	}
	v := app.ShareVersion{ID: rows[0].ClinicShareVersionID, VersionNo: rows[0].VersionNo, StatisticsFileID: rows[0].StatisticsFileID,
		Basis: rows[0].Basis, ValidFrom: rows[0].ValidFrom.Time}
	lines := make([]app.ShareLine, 0, len(rows))
	for _, r := range rows {
		share, err := numericShare(r.ClinicShare)
		if err != nil {
			return app.ShareVersion{}, nil, err
		}
		lines = append(lines, app.ShareLine{DistrictID: r.DistrictID, ClinicID: r.ClinicID, Share: share})
	}
	return v, lines, nil
}

func fileFromRow(r gen.StatisticsFile) app.FileInfo {
	f := app.FileInfo{
		ID: r.StatisticsFileID, Kind: domain.FileKind(r.FileKind), FileName: r.FileName, Checksum: r.FileChecksum,
		Status: domain.CheckStatus(r.CheckStatus), RejectedRows: int(r.RejectedRows), UploadedBy: r.UploadedBy,
		UploadedAt: r.UploadedAt.Time,
	}
	if r.DataSource != nil {
		f.DataSource = *r.DataSource
	}
	if r.PeriodStart.Valid && r.PeriodEnd.Valid {
		f.Period = fromDates(r.PeriodStart, r.PeriodEnd)
	}
	return f
}

func toDate(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func fromDates(start, end pgtype.Date) domain.Period {
	return domain.Period{Start: start.Time.UTC(), End: end.Time.UTC()}
}

// shareNumeric переводит долю в numeric(5,4) точно: целые десятитысячные со степенью -4.
func shareNumeric(s domain.Share) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(s.TenThousandths()), Exp: -4, Valid: true}
}

// numericShare читает numeric(5,4) обратно в долю без двоичной погрешности.
func numericShare(n pgtype.Numeric) (domain.Share, error) {
	if !n.Valid || n.Int == nil {
		return 0, errors.New("доля не задана")
	}
	units := new(big.Int).Set(n.Int)
	switch shift := int64(n.Exp) + 4; {
	case shift > 0:
		units.Mul(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(shift), nil))
	case shift < 0:
		units.Quo(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(-shift), nil))
	}
	if !units.IsInt64() {
		return 0, errors.New("доля вне диапазона")
	}
	return domain.Share(float64(units.Int64()) / 10000), nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func clampInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}
