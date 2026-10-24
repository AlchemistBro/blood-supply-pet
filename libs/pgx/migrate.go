package pgx

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Migrate применяет goose-миграции из нескольких файловых систем (платформенные из libs и
// собственные сервиса) к базе пула. Сессионная блокировка goose защищает от параллельного
// запуска несколькими экземплярами сервиса. Миграции встроены в бинарник (embed).
//
// Нумерация: 00001–00099 — платформа (outbox, inbox, аудит, справочники), 00100+ — сервис.
// Пропущенные платформенные миграции применяются и тогда, когда версия базы уже выше (out-of-order).
func Migrate(ctx context.Context, pool *pgxpool.Pool, sources ...fs.FS) ([]string, error) {
	merged, err := MergeFS(sources...)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(5, 60))
	if err != nil {
		return nil, fmt.Errorf("session locker: %w", err)
	}
	// Платформенный диапазон 00001–00099 пополняется после того, как у сервисов уже есть свои 00100+:
	// новая платформенная миграция (например, 00003 справочника статусов единицы) меньше версии базы.
	// Без разрешения «не по порядку» goose откажется её применять, и сервис не стартует на существующем томе.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, merged,
		goose.WithSessionLocker(locker), goose.WithAllowOutofOrder(true))
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("применение миграций: %w", err)
	}
	applied := make([]string, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Path)
	}
	return applied, nil
}

// MergeFS объединяет плоские файловые системы миграций в одну: goose читает корень.
// Дубликаты имён — ошибка, чтобы платформа и сервис не затирали друг друга.
func MergeFS(sources ...fs.FS) (fs.FS, error) {
	m := mergedFS{files: map[string]fs.FS{}}
	for _, src := range sources {
		entries, err := fs.ReadDir(src, ".")
		if err != nil {
			return nil, fmt.Errorf("чтение миграций: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if _, dup := m.files[e.Name()]; dup {
				return nil, fmt.Errorf("миграция %s встречается дважды", e.Name())
			}
			m.files[e.Name()] = src
			m.names = append(m.names, e.Name())
		}
	}
	sort.Strings(m.names)
	return m, nil
}

type mergedFS struct {
	files map[string]fs.FS
	names []string
}

func (m mergedFS) Open(name string) (fs.File, error) {
	if name == "." {
		return &mergedDir{fs: m}, nil
	}
	src, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return src.Open(name)
}

func (m mergedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, 0, len(m.names))
	for _, n := range m.names {
		info, err := fs.Stat(m.files[n], n)
		if err != nil {
			return nil, err
		}
		out = append(out, fs.FileInfoToDirEntry(info))
	}
	return out, nil
}

type mergedDir struct {
	fs     mergedFS
	offset int
}

func (d *mergedDir) Stat() (fs.FileInfo, error) { return dirInfo{}, nil }
func (d *mergedDir) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }
func (d *mergedDir) Close() error               { return nil }

func (d *mergedDir) ReadDir(n int) ([]fs.DirEntry, error) {
	all, err := d.fs.ReadDir(".")
	if err != nil {
		return nil, err
	}
	rest := all[d.offset:]
	if n <= 0 || n >= len(rest) {
		d.offset = len(all)
		return rest, nil
	}
	d.offset += n
	return rest[:n], nil
}

type dirInfo struct{}

func (dirInfo) Name() string       { return "." }
func (dirInfo) Size() int64        { return 0 }
func (dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (dirInfo) ModTime() time.Time { return time.Time{} }
func (dirInfo) IsDir() bool        { return true }
func (dirInfo) Sys() any           { return nil }
