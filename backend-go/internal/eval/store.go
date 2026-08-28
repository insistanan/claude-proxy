package eval

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示评测存储中请求的对象不存在。调用方可据此区分“不存在”与
// 数据库锁、损坏或连接失败等真实存储错误，避免同步内置数据时误插入。
var ErrNotFound = errors.New("评测对象不存在")

// Store 评测题库与运行记录。路径约定与 metrics/conversations 一样落在 .config。
type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("eval database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// 单连接串行化写入。代价是：任何在 rows 游标未关闭时发起的第二条查询都会
	// 向连接池索要第二条连接，而池上限为 1 且等待不带超时，直接死锁并永久占住连接。
	// 因此本文件里的列表查询一律用 JOIN 一次取全，绝不在 rows.Next() 循环里再查库。
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS eval_probes (
			id TEXT PRIMARY KEY,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL,
			stimulus_json TEXT NOT NULL,
			extract_kind TEXT NOT NULL,
			extract_spec_json TEXT NOT NULL,
			judge_kind TEXT NOT NULL,
			judge_spec_json TEXT NOT NULL,
			sample_count INTEGER NOT NULL DEFAULT 1,
			applicable_service_types_json TEXT NOT NULL DEFAULT '[]',
			cheap INTEGER NOT NULL DEFAULT 0,
			builtin INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS eval_suites (
			id TEXT PRIMARY KEY,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			probe_ids_json TEXT NOT NULL,
			cheap INTEGER NOT NULL DEFAULT 0,
			builtin INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS eval_runs (
			id TEXT PRIMARY KEY,
			suite_id TEXT NOT NULL,
			trigger TEXT NOT NULL,
			status TEXT NOT NULL,
			channel_ids_json TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			channel_models_json TEXT NOT NULL DEFAULT '{}',
			thinking TEXT NOT NULL DEFAULT '',
			estimated_calls INTEGER NOT NULL DEFAULT 0,
			skip_reason TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL DEFAULT 0,
			finished_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS eval_results (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			probe_id TEXT NOT NULL,
			verdict TEXT NOT NULL,
			excerpt TEXT NOT NULL DEFAULT '',
			detail_json TEXT NOT NULL DEFAULT '{}',
			latency_ms INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (run_id) REFERENCES eval_runs(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_results_run ON eval_results(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_results_channel ON eval_results(channel_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS eval_watch (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			enabled INTEGER NOT NULL DEFAULT 0,
			suite_id TEXT NOT NULL DEFAULT '',
			interval TEXT NOT NULL DEFAULT '2h',
			channel_ids_json TEXT NOT NULL DEFAULT '[]',
			model TEXT NOT NULL DEFAULT '',
			thinking TEXT NOT NULL DEFAULT '',
			last_run_at INTEGER NOT NULL DEFAULT 0,
			next_run_at INTEGER NOT NULL DEFAULT 0,
			last_skip_reason TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT OR IGNORE INTO eval_watch (id, enabled, suite_id, interval, channel_ids_json, model, thinking, last_run_at, next_run_at, last_skip_reason)
		 VALUES (1, 0, '', '2h', '[]', '', '', 0, 0, '')`,
		`CREATE TABLE IF NOT EXISTS eval_channel_latest (
			channel_id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			suite_id TEXT NOT NULL,
			suite_name TEXT NOT NULL DEFAULT '',
			aggregate TEXT NOT NULL,
			finished_at INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("eval migrate: %w", err)
		}
	}
	// 老库补列：这些列是后加的，早于它们建的库要就地升级。
	addedColumns := []struct {
		table      string
		column     string
		definition string
	}{
		{"eval_probes", "builtin", "INTEGER NOT NULL DEFAULT 0"},
		{"eval_suites", "builtin", "INTEGER NOT NULL DEFAULT 0"},
		{"eval_probes", "description", "TEXT NOT NULL DEFAULT ''"},
		{"eval_suites", "description", "TEXT NOT NULL DEFAULT ''"},
		{"eval_runs", "channel_models_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"eval_watch", "channel_models_json", "TEXT NOT NULL DEFAULT '{}'"},
	}
	for _, item := range addedColumns {
		if err := s.ensureColumn(item.table, item.column, item.definition); err != nil {
			return fmt.Errorf("eval migrate: %w", err)
		}
	}
	return nil
}

// ensureColumn 幂等补列。SQLite 没有 ADD COLUMN IF NOT EXISTS，先查 pragma 再决定加不加。
// table / column / definition 都是包内常量，不来自外部输入。
func (s *Store) ensureColumn(table, column, definition string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func newEvalID(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err == nil {
		return prefix + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
}

func marshalJSON(value interface{}) (string, error) {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func unmarshalJSON(raw string, dest interface{}) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), dest)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// probeColumns / suiteColumns 是列清单的唯一出处：加列时只改这里 + scan 函数，
// 免得三处 SELECT 各写一遍再漏掉一处。
const probeColumns = `id, slug, name, description, category, stimulus_json, extract_kind, extract_spec_json, judge_kind, judge_spec_json, sample_count, applicable_service_types_json, cheap, builtin, created_at, updated_at`

const suiteColumns = `id, slug, name, description, probe_ids_json, cheap, builtin, created_at, updated_at`

// marshalProbe 把探针的四个 JSON 字段一次性序列化，Insert 与 Update 共用。
func marshalProbe(probe Probe) (stimulus, extract, judge, serviceTypes string, err error) {
	if stimulus, err = marshalJSON(probe.Stimulus); err != nil {
		return "", "", "", "", err
	}
	if extract, err = marshalJSON(probe.Extract); err != nil {
		return "", "", "", "", err
	}
	if judge, err = marshalJSON(probe.Judge); err != nil {
		return "", "", "", "", err
	}
	if serviceTypes, err = marshalJSON(probe.ApplicableServiceTypes); err != nil {
		return "", "", "", "", err
	}
	return stimulus, extract, judge, serviceTypes, nil
}

func (s *Store) InsertProbe(probe Probe) (Probe, error) {
	report := ValidateProbe(probe)
	if !report.OK {
		return Probe{}, fmt.Errorf("%s", strings.Join(report.Errors, "; "))
	}
	now := time.Now().Unix()
	if probe.ID == "" {
		probe.ID = newEvalID("pr_")
	}
	if probe.CreatedAt == 0 {
		probe.CreatedAt = now
	}
	probe.UpdatedAt = now
	probe.Cheap = probeIsCheap(probe)
	stimulusJSON, extractJSON, judgeJSON, typesJSON, err := marshalProbe(probe)
	if err != nil {
		return Probe{}, err
	}
	_, err = s.db.Exec(
		`INSERT INTO eval_probes (`+probeColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		probe.ID, probe.Slug, probe.Name, probe.Description, probe.Category, stimulusJSON, probe.Extract.Kind, extractJSON, probe.Judge.Kind, judgeJSON, probe.SampleCount, typesJSON, boolToInt(probe.Cheap), boolToInt(probe.Builtin), probe.CreatedAt, probe.UpdatedAt,
	)
	if err != nil {
		return Probe{}, err
	}
	return probe, nil
}

// SyncBuiltinProbe 按 slug 把内置题同步成代码里的定义：不存在就插入，存在就整条覆盖。
// 保留原 ID 与创建时间，套件引用不会断。只给 seed 用，外部改内置题一律走 UpdateProbe 被拒。
func (s *Store) SyncBuiltinProbe(probe Probe) error {
	probe.Builtin = true
	existing, err := s.GetProbeBySlug(probe.Slug)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("读取内置探针 %s 失败: %w", probe.Slug, err)
		}
		if _, insertErr := s.InsertProbe(probe); insertErr != nil {
			return insertErr
		}
		return nil
	}
	probe.ID = existing.ID
	probe.CreatedAt = existing.CreatedAt
	return s.writeProbe(probe)
}

func (s *Store) UpdateProbe(probe Probe) (Probe, error) {
	if probe.ID == "" {
		return Probe{}, fmt.Errorf("更新探针必须带 id")
	}
	existing, err := s.GetProbe(probe.ID)
	if err != nil {
		return Probe{}, err
	}
	if existing.Builtin {
		return Probe{}, fmt.Errorf("内置题目由代码维护，改不了；复制成自建题再改")
	}
	report := ValidateProbe(probe)
	if !report.OK {
		return Probe{}, fmt.Errorf("%s", strings.Join(report.Errors, "; "))
	}
	probe.Builtin = false
	probe.CreatedAt = existing.CreatedAt
	if err := s.writeProbe(probe); err != nil {
		return Probe{}, err
	}
	return s.GetProbe(probe.ID)
}

// writeProbe 落盘一条已存在的探针。内置保护在调用方做——seed 同步要绕过它。
func (s *Store) writeProbe(probe Probe) error {
	probe.UpdatedAt = time.Now().Unix()
	probe.Cheap = probeIsCheap(probe)
	stimulusJSON, extractJSON, judgeJSON, typesJSON, err := marshalProbe(probe)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(
		`UPDATE eval_probes SET slug=?, name=?, description=?, category=?, stimulus_json=?, extract_kind=?, extract_spec_json=?, judge_kind=?, judge_spec_json=?, sample_count=?, applicable_service_types_json=?, cheap=?, builtin=?, updated_at=? WHERE id=?`,
		probe.Slug, probe.Name, probe.Description, probe.Category, stimulusJSON, probe.Extract.Kind, extractJSON, probe.Judge.Kind, judgeJSON, probe.SampleCount, typesJSON, boolToInt(probe.Cheap), boolToInt(probe.Builtin), probe.UpdatedAt, probe.ID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: 探针不存在", ErrNotFound)
	}
	return nil
}

func (s *Store) DeleteProbe(id string) error {
	existing, err := s.GetProbe(id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return fmt.Errorf("内置题目由代码维护，删不了")
	}
	result, err := s.db.Exec(`DELETE FROM eval_probes WHERE id=?`, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: 探针不存在", ErrNotFound)
	}
	return nil
}

func scanProbe(scanner interface {
	Scan(dest ...any) error
}) (Probe, error) {
	var probe Probe
	var stimulusJSON, extractJSON, judgeJSON, typesJSON string
	var cheap, builtin int
	err := scanner.Scan(
		&probe.ID, &probe.Slug, &probe.Name, &probe.Description, &probe.Category,
		&stimulusJSON, &probe.Extract.Kind, &extractJSON, &probe.Judge.Kind, &judgeJSON,
		&probe.SampleCount, &typesJSON, &cheap, &builtin, &probe.CreatedAt, &probe.UpdatedAt,
	)
	if err != nil {
		return Probe{}, err
	}
	if err := unmarshalJSON(stimulusJSON, &probe.Stimulus); err != nil {
		return Probe{}, err
	}
	if err := unmarshalJSON(extractJSON, &probe.Extract); err != nil {
		return Probe{}, err
	}
	if err := unmarshalJSON(judgeJSON, &probe.Judge); err != nil {
		return Probe{}, err
	}
	if err := unmarshalJSON(typesJSON, &probe.ApplicableServiceTypes); err != nil {
		return Probe{}, err
	}
	if probe.ApplicableServiceTypes == nil {
		probe.ApplicableServiceTypes = []string{}
	}
	probe.Cheap = cheap == 1
	probe.Builtin = builtin == 1
	return probe, nil
}

func (s *Store) GetProbe(id string) (Probe, error) {
	row := s.db.QueryRow(`SELECT `+probeColumns+` FROM eval_probes WHERE id=?`, id)
	probe, err := scanProbe(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Probe{}, fmt.Errorf("%w: 探针不存在", ErrNotFound)
	}
	return probe, err
}

func (s *Store) GetProbeBySlug(slug string) (Probe, error) {
	row := s.db.QueryRow(`SELECT `+probeColumns+` FROM eval_probes WHERE slug=?`, slug)
	probe, err := scanProbe(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Probe{}, fmt.Errorf("%w: 探针不存在", ErrNotFound)
	}
	return probe, err
}

func (s *Store) ListProbes() ([]Probe, error) {
	rows, err := s.db.Query(`SELECT ` + probeColumns + ` FROM eval_probes ORDER BY category, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var probes []Probe
	for rows.Next() {
		probe, err := scanProbe(rows)
		if err != nil {
			return nil, err
		}
		probes = append(probes, probe)
	}
	if probes == nil {
		probes = []Probe{}
	}
	return probes, rows.Err()
}

func (s *Store) InsertSuite(suite Suite) (Suite, error) {
	now := time.Now().Unix()
	if suite.ID == "" {
		suite.ID = newEvalID("su_")
	}
	if suite.CreatedAt == 0 {
		suite.CreatedAt = now
	}
	suite.UpdatedAt = now
	probeIDsJSON, err := marshalJSON(suite.ProbeIDs)
	if err != nil {
		return Suite{}, err
	}
	_, err = s.db.Exec(
		`INSERT INTO eval_suites (`+suiteColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		suite.ID, suite.Slug, suite.Name, suite.Description, probeIDsJSON, boolToInt(suite.Cheap), boolToInt(suite.Builtin), suite.CreatedAt, suite.UpdatedAt,
	)
	if err != nil {
		return Suite{}, err
	}
	return suite, nil
}

// SyncBuiltinSuite 与 SyncBuiltinProbe 同理：内置套件的成员清单跟着代码走。
func (s *Store) SyncBuiltinSuite(suite Suite) error {
	suite.Builtin = true
	existing, err := s.GetSuiteBySlug(suite.Slug)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("读取内置套件 %s 失败: %w", suite.Slug, err)
		}
		if _, insertErr := s.InsertSuite(suite); insertErr != nil {
			return insertErr
		}
		return nil
	}
	suite.ID = existing.ID
	suite.CreatedAt = existing.CreatedAt
	return s.writeSuite(suite)
}

func (s *Store) UpdateSuite(suite Suite) (Suite, error) {
	if suite.ID == "" {
		return Suite{}, fmt.Errorf("更新套件必须带 id")
	}
	existing, err := s.GetSuite(suite.ID)
	if err != nil {
		return Suite{}, err
	}
	if existing.Builtin {
		return Suite{}, fmt.Errorf("内置套件由代码维护，改不了；复制成自建套件再改")
	}
	suite.Builtin = false
	suite.CreatedAt = existing.CreatedAt
	if err := s.writeSuite(suite); err != nil {
		return Suite{}, err
	}
	return s.GetSuite(suite.ID)
}

func (s *Store) writeSuite(suite Suite) error {
	suite.UpdatedAt = time.Now().Unix()
	probeIDsJSON, err := marshalJSON(suite.ProbeIDs)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(
		`UPDATE eval_suites SET slug=?, name=?, description=?, probe_ids_json=?, cheap=?, builtin=?, updated_at=? WHERE id=?`,
		suite.Slug, suite.Name, suite.Description, probeIDsJSON, boolToInt(suite.Cheap), boolToInt(suite.Builtin), suite.UpdatedAt, suite.ID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: 套件不存在", ErrNotFound)
	}
	return nil
}

func (s *Store) DeleteSuite(id string) error {
	existing, err := s.GetSuite(id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return fmt.Errorf("内置套件由代码维护，删不了")
	}
	result, err := s.db.Exec(`DELETE FROM eval_suites WHERE id=?`, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: 套件不存在", ErrNotFound)
	}
	return nil
}

func scanSuite(scanner interface {
	Scan(dest ...any) error
}) (Suite, error) {
	var suite Suite
	var probeIDsJSON string
	var cheap, builtin int
	if err := scanner.Scan(&suite.ID, &suite.Slug, &suite.Name, &suite.Description, &probeIDsJSON, &cheap, &builtin, &suite.CreatedAt, &suite.UpdatedAt); err != nil {
		return Suite{}, err
	}
	if err := unmarshalJSON(probeIDsJSON, &suite.ProbeIDs); err != nil {
		return Suite{}, err
	}
	if suite.ProbeIDs == nil {
		suite.ProbeIDs = []string{}
	}
	suite.Cheap = cheap == 1
	suite.Builtin = builtin == 1
	return suite, nil
}

func (s *Store) GetSuite(id string) (Suite, error) {
	row := s.db.QueryRow(`SELECT `+suiteColumns+` FROM eval_suites WHERE id=?`, id)
	suite, err := scanSuite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Suite{}, fmt.Errorf("%w: 套件不存在", ErrNotFound)
	}
	return suite, err
}

func (s *Store) GetSuiteBySlug(slug string) (Suite, error) {
	row := s.db.QueryRow(`SELECT `+suiteColumns+` FROM eval_suites WHERE slug=?`, slug)
	suite, err := scanSuite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Suite{}, fmt.Errorf("%w: 套件不存在", ErrNotFound)
	}
	return suite, err
}

func (s *Store) ListSuites() ([]Suite, error) {
	rows, err := s.db.Query(`SELECT ` + suiteColumns + ` FROM eval_suites ORDER BY cheap DESC, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var suites []Suite
	for rows.Next() {
		suite, err := scanSuite(rows)
		if err != nil {
			return nil, err
		}
		suites = append(suites, suite)
	}
	if suites == nil {
		suites = []Suite{}
	}
	return suites, rows.Err()
}

// probesForSuite 按 ProbeIDs 顺序一次性取全探针，单条 JOIN 不带游标内再查库
// （见 NewStore 的单连接说明：循环 GetProbe 会因独占连接死锁）。
// 缺任何一条 ProbeID 即报错，不静默跳过。
func (s *Store) probesForSuite(suiteID string, probeIDs []string) ([]Probe, error) {
	if len(probeIDs) == 0 {
		return []Probe{}, nil
	}
	placeholders := make([]string, len(probeIDs))
	args := make([]any, 0, len(probeIDs))
	for index, id := range probeIDs {
		placeholders[index] = "?"
		args = append(args, id)
	}
	rows, err := s.db.Query(
		`SELECT p.`+probeColumns+`
		 FROM eval_probes p
		 WHERE p.id IN (`+strings.Join(placeholders, ",")+`)
		`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[string]Probe, len(probeIDs))
	for rows.Next() {
		probe, scanErr := scanProbe(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		byID[probe.ID] = probe
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	probes := make([]Probe, 0, len(probeIDs))
	for _, id := range probeIDs {
		probe, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("套件 %s 引用的探针 %s 缺失", suiteID, id)
		}
		probes = append(probes, probe)
	}
	return probes, nil
}

// ProbesForSuite 按套件里的 probeIds 顺序取出探针。缺任何一条即报错，不静默跳过。
func (s *Store) ProbesForSuite(suite Suite) ([]Probe, error) {
	return s.probesForSuite(suite.ID, suite.ProbeIDs)
}

// SuiteWithProbes 是 GetSuite + ProbesForSuite 的组合，供 estimate / validate / 值班共用。
func (s *Store) SuiteWithProbes(suiteID string) (Suite, []Probe, error) {
	suite, err := s.GetSuite(suiteID)
	if err != nil {
		return Suite{}, nil, err
	}
	probes, err := s.ProbesForSuite(suite)
	if err != nil {
		return Suite{}, nil, err
	}
	return suite, probes, nil
}

func (s *Store) CreateRun(run Run) (Run, error) {
	if run.ID == "" {
		run.ID = newEvalID("run_")
	}
	if run.CreatedAt == 0 {
		run.CreatedAt = time.Now().Unix()
	}
	if run.Status == "" {
		run.Status = RunQueued
	}
	channelIDsJSON, channelModelsJSON, err := marshalRunChannels(run)
	if err != nil {
		return Run{}, err
	}
	_, err = s.db.Exec(
		`INSERT INTO eval_runs (`+runColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.SuiteID, run.Trigger, run.Status, channelIDsJSON, run.Model, channelModelsJSON, run.Thinking, run.EstimatedCalls, run.SkipReason, run.Error, run.StartedAt, run.FinishedAt, run.CreatedAt,
	)
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *Store) UpdateRun(run Run) error {
	channelIDsJSON, channelModelsJSON, err := marshalRunChannels(run)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(
		`UPDATE eval_runs SET suite_id=?, trigger=?, status=?, channel_ids_json=?, model=?, channel_models_json=?, thinking=?, estimated_calls=?, skip_reason=?, error=?, started_at=?, finished_at=? WHERE id=?`,
		run.SuiteID, run.Trigger, run.Status, channelIDsJSON, run.Model, channelModelsJSON, run.Thinking, run.EstimatedCalls, run.SkipReason, run.Error, run.StartedAt, run.FinishedAt, run.ID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: 评测批次不存在", ErrNotFound)
	}
	return nil
}

func marshalRunChannels(run Run) (channelIDs, channelModels string, err error) {
	if channelIDs, err = marshalJSON(run.ChannelIDs); err != nil {
		return "", "", err
	}
	if run.ChannelModels == nil {
		run.ChannelModels = map[string]string{}
	}
	if channelModels, err = marshalJSON(run.ChannelModels); err != nil {
		return "", "", err
	}
	return channelIDs, channelModels, nil
}

// runColumns 是 eval_runs 的列清单；runColumnsWithSuiteName 额外带上 JOIN 出来的套件名，
// 供 scanRunWithSuiteName 消费。加列时改这两处 + scan 函数即可。
const runColumns = `id, suite_id, trigger, status, channel_ids_json, model, channel_models_json, thinking, estimated_calls, skip_reason, error, started_at, finished_at, created_at`

const runColumnsWithSuiteName = `r.id, r.suite_id, r.trigger, r.status, r.channel_ids_json, r.model, r.channel_models_json, r.thinking, r.estimated_calls, r.skip_reason, r.error, r.started_at, r.finished_at, r.created_at, COALESCE(s.name, '')`

// scanRunWithSuiteName 读一行 run，最后一列是 JOIN 出来的套件名。
// 套件名必须靠 JOIN 拿，不能在游标里再查库（见 NewStore 的单连接说明）。
func scanRunWithSuiteName(scanner interface {
	Scan(dest ...any) error
}) (Run, error) {
	var run Run
	var channelIDsJSON, channelModelsJSON string
	if err := scanner.Scan(
		&run.ID, &run.SuiteID, &run.Trigger, &run.Status, &channelIDsJSON, &run.Model, &channelModelsJSON,
		&run.Thinking, &run.EstimatedCalls, &run.SkipReason, &run.Error, &run.StartedAt, &run.FinishedAt, &run.CreatedAt,
		&run.SuiteName,
	); err != nil {
		return Run{}, err
	}
	if err := unmarshalJSON(channelIDsJSON, &run.ChannelIDs); err != nil {
		return Run{}, err
	}
	if run.ChannelIDs == nil {
		run.ChannelIDs = []string{}
	}
	if err := unmarshalJSON(channelModelsJSON, &run.ChannelModels); err != nil {
		return Run{}, err
	}
	if run.ChannelModels == nil {
		run.ChannelModels = map[string]string{}
	}
	return run, nil
}

func (s *Store) GetRun(id string) (Run, error) {
	row := s.db.QueryRow(
		`SELECT `+runColumnsWithSuiteName+`
		 FROM eval_runs r LEFT JOIN eval_suites s ON s.id = r.suite_id WHERE r.id=?`, id)
	run, err := scanRunWithSuiteName(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("%w: 评测批次不存在", ErrNotFound)
	}
	if err != nil {
		return Run{}, err
	}
	results, err := s.ListResults(run.ID)
	if err != nil {
		return Run{}, err
	}
	run.Results = results
	return run, nil
}

func (s *Store) ListRuns(limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT `+runColumnsWithSuiteName+`
		 FROM eval_runs r LEFT JOIN eval_suites s ON s.id = r.suite_id
		 ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		run, err := scanRunWithSuiteName(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if runs == nil {
		runs = []Run{}
	}
	return runs, rows.Err()
}

// DeleteRun 删除一个批次。结果表有 ON DELETE CASCADE，只需删主表。
func (s *Store) DeleteRun(id string) error {
	result, err := s.db.Exec(`DELETE FROM eval_runs WHERE id=?`, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: 评测批次不存在", ErrNotFound)
	}
	return nil
}

// RecoverStaleRuns 把上次进程留下的 queued / running 批次盖上终态。
// Runner 的 busy 只存在内存里，崩溃或重启后 DB 里的"进行中"没人收尾，
// 前端会一直显示幽灵批次并且按钮永远停在取消态。
func (s *Store) RecoverStaleRuns() (int64, error) {
	result, err := s.db.Exec(
		`UPDATE eval_runs SET status=?, error=CASE WHEN error='' THEN ? ELSE error END, finished_at=?
		 WHERE status IN (?, ?)`,
		RunCancelled, "进程重启，批次未跑完", time.Now().Unix(), RunQueued, RunRunning,
	)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

func (s *Store) InsertResult(result Result) (Result, error) {
	if result.ID == "" {
		result.ID = newEvalID("rs_")
	}
	if result.CreatedAt == 0 {
		result.CreatedAt = time.Now().Unix()
	}
	if result.Detail == nil {
		result.Detail = map[string]interface{}{}
	}
	detailJSON, err := marshalJSON(result.Detail)
	if err != nil {
		return Result{}, err
	}
	_, err = s.db.Exec(
		`INSERT INTO eval_results (id, run_id, channel_id, probe_id, verdict, excerpt, detail_json, latency_ms, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.ID, result.RunID, result.ChannelID, result.ProbeID, result.Verdict, result.Excerpt, detailJSON, result.LatencyMS, result.CreatedAt,
	)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Store) ListResults(runID string) ([]Result, error) {
	rows, err := s.db.Query(
		`SELECT rs.id, rs.run_id, rs.channel_id, rs.probe_id, rs.verdict, rs.excerpt, rs.detail_json, rs.latency_ms, rs.created_at, COALESCE(p.name, '')
		 FROM eval_results rs LEFT JOIN eval_probes p ON p.id = rs.probe_id
		 WHERE rs.run_id=? ORDER BY rs.created_at, rs.id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []Result
	for rows.Next() {
		var result Result
		var detailJSON string
		if err := rows.Scan(&result.ID, &result.RunID, &result.ChannelID, &result.ProbeID, &result.Verdict, &result.Excerpt, &detailJSON, &result.LatencyMS, &result.CreatedAt, &result.ProbeName); err != nil {
			return nil, err
		}
		if err := unmarshalJSON(detailJSON, &result.Detail); err != nil {
			return nil, err
		}
		if result.Detail == nil {
			result.Detail = map[string]interface{}{}
		}
		results = append(results, result)
	}
	if results == nil {
		results = []Result{}
	}
	return results, rows.Err()
}

// UpdateResultDetail 仅覆盖 detail_json。批次跑完后的跨渠道指纹比对用它回填，不动 verdict。
func (s *Store) UpdateResultDetail(id string, detail map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("结果 id 不能为空")
	}
	if detail == nil {
		detail = map[string]interface{}{}
	}
	detailJSON, err := marshalJSON(detail)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE eval_results SET detail_json=? WHERE id=?`, detailJSON, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: 结果 %s 不存在", ErrNotFound, id)
	}
	return nil
}

func (s *Store) GetWatch() (WatchConfig, error) {
	row := s.db.QueryRow(`SELECT enabled, suite_id, interval, channel_ids_json, model, channel_models_json, thinking, last_run_at, next_run_at, last_skip_reason FROM eval_watch WHERE id=1`)
	var watch WatchConfig
	var enabled int
	var channelIDsJSON, channelModelsJSON string
	if err := row.Scan(&enabled, &watch.SuiteID, &watch.Interval, &channelIDsJSON, &watch.Model, &channelModelsJSON, &watch.Thinking, &watch.LastRunAt, &watch.NextRunAt, &watch.LastSkipReason); err != nil {
		return WatchConfig{}, err
	}
	watch.Enabled = enabled == 1
	if err := unmarshalJSON(channelIDsJSON, &watch.ChannelIDs); err != nil {
		return WatchConfig{}, err
	}
	if watch.ChannelIDs == nil {
		watch.ChannelIDs = []string{}
	}
	if err := unmarshalJSON(channelModelsJSON, &watch.ChannelModels); err != nil {
		return WatchConfig{}, err
	}
	if watch.ChannelModels == nil {
		watch.ChannelModels = map[string]string{}
	}
	if watch.Interval == "" {
		watch.Interval = Interval2h
	}
	return watch, nil
}

func (s *Store) PutWatch(watch WatchConfig) error {
	channelIDsJSON, err := marshalJSON(watch.ChannelIDs)
	if err != nil {
		return err
	}
	if watch.ChannelModels == nil {
		watch.ChannelModels = map[string]string{}
	}
	channelModelsJSON, err := marshalJSON(watch.ChannelModels)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE eval_watch SET enabled=?, suite_id=?, interval=?, channel_ids_json=?, model=?, channel_models_json=?, thinking=?, last_run_at=?, next_run_at=?, last_skip_reason=? WHERE id=1`,
		boolToInt(watch.Enabled), watch.SuiteID, watch.Interval, channelIDsJSON, watch.Model, channelModelsJSON, watch.Thinking, watch.LastRunAt, watch.NextRunAt, watch.LastSkipReason,
	)
	return err
}

func (s *Store) SaveChannelLatest(latest ChannelLatest, suiteID string) error {
	_, err := s.db.Exec(
		`INSERT INTO eval_channel_latest (channel_id, run_id, suite_id, suite_name, aggregate, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel_id) DO UPDATE SET run_id=excluded.run_id, suite_id=excluded.suite_id, suite_name=excluded.suite_name, aggregate=excluded.aggregate, finished_at=excluded.finished_at`,
		latest.ChannelID, latest.RunID, suiteID, latest.SuiteName, latest.Aggregate, latest.FinishedAt,
	)
	return err
}

func (s *Store) ListChannelLatest() ([]ChannelLatest, error) {
	rows, err := s.db.Query(`SELECT channel_id, run_id, suite_id, suite_name, aggregate, finished_at FROM eval_channel_latest`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ChannelLatest
	for rows.Next() {
		var item ChannelLatest
		var suiteID string
		if err := rows.Scan(&item.ChannelID, &item.RunID, &suiteID, &item.SuiteName, &item.Aggregate, &item.FinishedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []ChannelLatest{}
	}
	return items, rows.Err()
}
