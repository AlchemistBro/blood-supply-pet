// Package api — HTTP-обработчики сервиса demand по контракту api/openapi.yaml (строгий сервер oapi-codegen).
package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/domain"
)

// Ограничения загрузки: файл — не больше предела домена (docs/ui/csv-formats.md); тело запроса чуть больше
// на заголовки частей multipart. Лимит тела не даёт читать в память гигантский запрос.
const (
	MaxFileBytes    = domain.MaxFileBytes
	maxRequestBytes = MaxFileBytes + 64<<10
	maxKindBytes    = 64
)

// operations — матрица «маршрут → операция прав»; совпадает с x-bs-operation в openapi.yaml (проверяет тест).
var operations = map[string]auth.Operation{
	"POST /files":            auth.OpUploadStatistics,
	"GET /files":             auth.OpUploadStatistics,
	"GET /files/{id}":        auth.OpUploadStatistics,
	"GET /files/{id}/errors": auth.OpUploadStatistics,
	"GET /history":           auth.OpUploadStatistics,
	"GET /shares/versions":   auth.OpUploadStatistics,
	"GET /shares/current":    auth.OpUploadStatistics,
}

// Handler реализует gen.StrictServerInterface.
type Handler struct {
	svc *app.Service
}

var _ gen.StrictServerInterface = (*Handler)(nil)

// Mount регистрирует маршруты на mux с проверкой прав по операции и лимитом тела загрузки.
func Mount(mux *http.ServeMux, svc *app.Service, onDenied auth.Denied) {
	h := &Handler{svc: svc}
	strict := gen.NewStrictHandlerWithOptions(h, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос",
				"Запрос не разобран. Для загрузки нужна форма multipart/form-data с полями file_kind и file.")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "Внутренняя ошибка", "Запрос не выполнен. Повторите позже.")
		},
	})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter:  mux,
		Middlewares: []gen.MiddlewareFunc{limitBody, authorize(onDenied)},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", "Проверьте параметры запроса.")
		},
	})
}

// authorize выбирает операцию матрицы прав по шаблону маршрута (r.Pattern заполняет ServeMux).
func authorize(onDenied auth.Denied) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, ok := operations[r.Pattern]
			if !ok {
				httpx.WriteProblem(w, r, http.StatusForbidden, "Недостаточно прав", "Маршрут не описан в матрице прав.")
				return
			}
			auth.Require(op, onDenied)(next).ServeHTTP(w, r)
		})
	}
}

// limitBody ограничивает тело запроса: большой файл не читается в память целиком.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// UploadFile — POST /files.
func (h *Handler) UploadFile(ctx context.Context, req gen.UploadFileRequestObject) (gen.UploadFileResponseObject, error) {
	var (
		kind     string
		fileName string
		content  []byte
		hasFile  bool
	)
	for {
		part, err := req.Body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return uploadReadError(err), nil
		}
		switch part.FormName() {
		case "file_kind":
			b, err := io.ReadAll(io.LimitReader(part, maxKindBytes))
			if err != nil {
				return uploadReadError(err), nil
			}
			kind = strings.TrimSpace(string(b))
		case "file":
			fileName = part.FileName()
			content, err = io.ReadAll(io.LimitReader(part, MaxFileBytes+1))
			if err != nil {
				return uploadReadError(err), nil
			}
			if len(content) > MaxFileBytes {
				return tooLarge(), nil
			}
			hasFile = true
		default:
			if _, err := io.Copy(io.Discard, part); err != nil {
				return uploadReadError(err), nil
			}
		}
		_ = part.Close()
	}
	if !hasFile {
		return gen.UploadFile400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(
			http.StatusBadRequest, "Нет файла", "Приложите файл CSV в поле file.")}, nil
	}
	fk, err := domain.ParseFileKind(kind)
	if err != nil {
		return gen.UploadFile400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(
			http.StatusBadRequest, "Не выбран вид файла",
			"Укажите вид файла: профиль районов (DISTRICT_PROFILE), история расхода (CONSUMPTION_HISTORY) или доли район → клиника (CLINIC_SHARE).")}, nil
	}
	p, _ := auth.FromContext(ctx)
	res, err := h.svc.Upload(ctx, app.UploadInput{Kind: fk, FileName: fileName, Content: content,
		Actor: app.Actor{UserID: userID(p), Role: p.PrimaryRole()}})
	var dup *app.DuplicateError
	switch {
	case errors.As(err, &dup):
		id := openapi_types.UUID(dup.ExistingID)
		return gen.UploadFile409ApplicationProblemPlusJSONResponse{
			Title: "Этот файл уже загружен", Status: http.StatusConflict, ExistingFileId: &id,
			Detail: ptr("Файл с таким же содержимым уже был загружен. Если данные изменились, загрузите исправленный файл."),
		}, nil
	case err != nil:
		return nil, err
	}
	out := gen.UploadResult{File: fileDetails(res.File), RowErrors: rowErrors(res.RowErrors)}
	if res.FileError != "" {
		out.FileError = ptr(res.FileError)
	}
	if res.ShareVersionNo > 0 {
		out.ShareVersionNo = ptr(res.ShareVersionNo)
	}
	return gen.UploadFile201JSONResponse(out), nil
}

func uploadReadError(err error) gen.UploadFileResponseObject {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return tooLarge()
	}
	return gen.UploadFile400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(
		http.StatusBadRequest, "Файл не получен", "Форма загрузки прочитана с ошибкой. Повторите загрузку.")}
}

func tooLarge() gen.UploadFileResponseObject {
	return gen.UploadFile413ApplicationProblemPlusJSONResponse{Title: "Файл слишком большой", Status: http.StatusRequestEntityTooLarge,
		Detail: ptr("Размер файла — не больше 5 МиБ. Разделите данные на несколько файлов.")}
}

// ListFiles — GET /files.
func (h *Handler) ListFiles(ctx context.Context, req gen.ListFilesRequestObject) (gen.ListFilesResponseObject, error) {
	var kind *domain.FileKind
	if req.Params.FileKind != nil {
		k := domain.FileKind(*req.Params.FileKind)
		kind = &k
	}
	limit := 100
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	files, err := h.svc.ListFiles(ctx, kind, limit)
	if err != nil {
		return nil, err
	}
	out := gen.ListFiles200JSONResponse{Items: make([]gen.FileSummary, 0, len(files))}
	for _, f := range files {
		out.Items = append(out.Items, fileSummary(f))
	}
	return out, nil
}

// GetFile — GET /files/{id}.
func (h *Handler) GetFile(ctx context.Context, req gen.GetFileRequestObject) (gen.GetFileResponseObject, error) {
	f, err := h.svc.GetFile(ctx, req.Id)
	if errors.Is(err, app.ErrNotFound) {
		return gen.GetFile404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: fileNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetFile200JSONResponse(fileDetails(f)), nil
}

// ListFileErrors — GET /files/{id}/errors.
func (h *Handler) ListFileErrors(ctx context.Context, req gen.ListFileErrorsRequestObject) (gen.ListFileErrorsResponseObject, error) {
	reasons, err := h.svc.FileErrors(ctx, req.Id)
	if errors.Is(err, app.ErrNotFound) {
		return gen.ListFileErrors404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: fileNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ListFileErrors200JSONResponse{Items: rowErrors(reasons)}, nil
}

// ListHistory — GET /history.
func (h *Handler) ListHistory(ctx context.Context, req gen.ListHistoryRequestObject) (gen.ListHistoryResponseObject, error) {
	records, err := h.svc.History(ctx, req.Params.ComponentKindCode)
	if err != nil {
		return nil, err
	}
	out := gen.ListHistory200JSONResponse{Items: make([]gen.HistoryRecord, 0, len(records))}
	for _, r := range records {
		item := gen.HistoryRecord{
			Id: r.ID, HistorySource: gen.HistoryRecordHistorySource(r.Source), ComponentKindCode: r.KindCode,
			PeriodStart: openapi_types.Date{Time: r.Period.Start}, PeriodEnd: openapi_types.Date{Time: r.Period.End},
			ConsumedVolumeMl: r.ConsumedVolumeML,
		}
		if r.StatisticsFileID != nil {
			id := *r.StatisticsFileID
			item.StatisticsFileId = &id
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ListShareVersions — GET /shares/versions.
func (h *Handler) ListShareVersions(ctx context.Context, _ gen.ListShareVersionsRequestObject) (gen.ListShareVersionsResponseObject, error) {
	versions, err := h.svc.ShareVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListShareVersions200JSONResponse{Items: make([]gen.ShareVersion, 0, len(versions))}
	for _, v := range versions {
		out.Items = append(out.Items, shareVersion(v))
	}
	return out, nil
}

// GetCurrentShares — GET /shares/current.
func (h *Handler) GetCurrentShares(ctx context.Context, _ gen.GetCurrentSharesRequestObject) (gen.GetCurrentSharesResponseObject, error) {
	v, lines, err := h.svc.CurrentShares(ctx)
	if errors.Is(err, app.ErrNotFound) {
		return gen.GetCurrentShares404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound(
			http.StatusNotFound, "Доли ещё не загружены", "Загрузите файл долей район → клиника (CLINIC_SHARE).")}, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.GetCurrentShares200JSONResponse{Version: shareVersion(v), Shares: make([]gen.ClinicShareLine, 0, len(lines))}
	for _, l := range lines {
		out.Shares = append(out.Shares, gen.ClinicShareLine{DistrictId: l.DistrictID, ClinicId: l.ClinicID, ClinicShare: float64(l.Share)})
	}
	return out, nil
}

func fileSummary(f app.FileInfo) gen.FileSummary {
	s := gen.FileSummary{
		Id: f.ID, FileKind: gen.FileKind(f.Kind), FileName: f.FileName, FileChecksum: f.Checksum,
		CheckStatus: gen.CheckStatus(f.Status), RejectedRows: f.RejectedRows, UploadedBy: f.UploadedBy, UploadedAt: f.UploadedAt,
	}
	if f.DataSource != "" {
		s.DataSource = ptr(f.DataSource)
	}
	if !f.Period.IsZero() {
		s.PeriodStart = &openapi_types.Date{Time: f.Period.Start}
		s.PeriodEnd = &openapi_types.Date{Time: f.Period.End}
	}
	return s
}

func fileDetails(f app.FileInfo) gen.FileDetails {
	s := fileSummary(f)
	return gen.FileDetails{
		Id: s.Id, FileKind: s.FileKind, FileName: s.FileName, FileChecksum: s.FileChecksum, CheckStatus: s.CheckStatus,
		RejectedRows: s.RejectedRows, UploadedBy: s.UploadedBy, UploadedAt: s.UploadedAt, DataSource: s.DataSource,
		PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd, AcceptedRows: f.AcceptedRows,
	}
}

func rowErrors(reasons []domain.RowReason) []gen.RowError {
	out := make([]gen.RowError, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, gen.RowError{RowNo: r.Row, Reason: r.Reason})
	}
	return out
}

func shareVersion(v app.ShareVersion) gen.ShareVersion {
	return gen.ShareVersion{Id: v.ID, VersionNo: v.VersionNo, StatisticsFileId: v.StatisticsFileID, Basis: v.Basis,
		ValidFrom: openapi_types.Date{Time: v.ValidFrom}}
}

func fileNotFound() gen.NotFoundApplicationProblemPlusJSONResponse {
	return notFound(http.StatusNotFound, "Файл не найден", "Проверьте ссылку: такого файла нет среди загруженных.")
}

func badRequest(status int, title, detail string) gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse{Title: title, Status: status, Detail: ptr(detail)}
}

func notFound(status int, title, detail string) gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse{Title: title, Status: status, Detail: ptr(detail)}
}

func userID(p auth.Principal) string {
	if p.Username != "" {
		return p.Username
	}
	return p.Subject
}

func ptr[T any](v T) *T { return &v }
