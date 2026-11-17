package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	serviceHeader = "# qqbot_bridge 服务配置：机器人凭据与白名单、CodeBuddy 启动参数、回传策略、管理端地址。\n" +
		"# 含机器人 AppSecret，权限应为 0600，不要提交到仓库。\n" +
		"# 手工修改后重启服务生效（模型除外）；程序自己改配置时会重写本文件，注释不会保留。\n"
	sessionsHeader = "# qqbot_bridge 会话配置：每个会话 = 一个工作目录 + 一段连续的 CodeBuddy 对话。\n" +
		"# 由 QQ 里的 /new /switch /rm /reset 等指令维护，一般不需要手工修改。\n"
)

// serviceFile 是 config.yaml 的内容。
type serviceFile struct {
	Bind     Bind     `yaml:"bind"`
	Agent    Agent    `yaml:"agent"`
	Push     Push     `yaml:"push"`
	Menu     Menu     `yaml:"menu"`
	Discover Discover `yaml:"discover"`
	Web      Web      `yaml:"web"`
}

// sessionsFile 是 sessions.yaml 的内容。
type sessionsFile struct {
	Current  string    `yaml:"current"`
	Sessions []Session `yaml:"sessions"`
	// Ignore 是用户主动移除过的自动发现目录。
	Ignore []string `yaml:"ignore,omitempty"`
}

// fileSig 用来发现文件在磁盘上被别的进程（比如 `qqbot_bridge bind`）改过。
type fileSig struct {
	mod  time.Time
	size int64
}

// Store 是带文件持久化的配置容器。
type Store struct {
	servicePath  string
	sessionsPath string

	mu  sync.Mutex
	cfg *Config

	// 最近一次读/写磁盘时的状态，用于只写变化的文件、以及发现外部修改
	svcSig, sessSig   fileSig
	svcData, sessData []byte

	migratedFrom string
}

// HomeEnv 是覆盖数据目录的环境变量，多实例部署时用它区分（如 ~/.qqbot_bridge-cursor）。
//
// 后台子进程由父进程 os.Environ 继承，所以命令行传 -home 时也会落成这个变量，
// 保证 daemon 化之后仍然用同一个目录。
const HomeEnv = "QQBOT_BRIDGE_HOME"

// dirOverride 是 -home 参数解析后的目录，优先于环境变量。
// 只在进程启动早期写一次，之后各 goroutine 并发读，所以用 atomic.Pointer。
var dirOverride atomic.Pointer[string]

// Dir 返回配置目录，优先级：SetDir 覆盖 > $QQBOT_BRIDGE_HOME > ~/.qqbot_bridge。
//
// pid 文件、日志、消息记录、管理端密码库、二维码都挂在这个目录下，
// 所以改成可覆盖之后，同一台机器上的多套实例天然互不干扰。
func Dir() string {
	if p := dirOverride.Load(); p != nil {
		return *p
	}
	if raw := strings.TrimSpace(os.Getenv(HomeEnv)); raw != "" {
		if abs, err := expandDir(raw); err == nil {
			return abs
		}
		return raw
	}
	return DefaultDir()
}

// DefaultDir 返回不指定 -home、也没有环境变量时的数据目录（~/.qqbot_bridge）。
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".qqbot_bridge"
	}
	return filepath.Join(home, ".qqbot_bridge")
}

// SetDir 覆盖数据目录，供 -home 参数使用；空串表示清除覆盖、回落到环境变量或默认值。
// 支持 ~/ 展开，相对路径按当前工作目录转成绝对路径。
func SetDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dirOverride.Store(nil)
		return nil
	}
	abs, err := expandDir(dir)
	if err != nil {
		return fmt.Errorf("数据目录 %q 不可用：%w", dir, err)
	}
	dirOverride.Store(&abs)
	return nil
}

// expandDir 展开开头的 ~ 并转成绝对路径。
func expandDir(dir string) (string, error) {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
	}
	return filepath.Abs(dir)
}

// Path 返回服务配置文件路径。
func Path() string { return filepath.Join(Dir(), "config.yaml") }

// SessionsPath 返回会话配置文件路径。
func SessionsPath() string { return filepath.Join(Dir(), "sessions.yaml") }

// Open 读取配置，文件不存在时返回默认配置。
func Open() (*Store, error) {
	return OpenAt(Path())
}

// OpenAt 读取以 servicePath 为服务配置的配置；会话配置固定放在同目录的 sessions.yaml。
//
// 两个文件都不存在、但同目录有旧版的 config.json 时，会自动迁移：
// 内容拆成两个 YAML 文件，旧文件改名为 config.json.migrated 留作备份。
func OpenAt(servicePath string) (*Store, error) {
	s := &Store{
		servicePath:  servicePath,
		sessionsPath: filepath.Join(filepath.Dir(servicePath), "sessions.yaml"),
		cfg:          Default(),
	}

	_, svcErr := os.Stat(s.servicePath)
	_, sessErr := os.Stat(s.sessionsPath)
	if errors.Is(svcErr, os.ErrNotExist) && errors.Is(sessErr, os.ErrNotExist) {
		legacy := filepath.Join(filepath.Dir(servicePath), "config.json")
		if _, err := s.migrateLegacy(legacy); err != nil {
			return nil, fmt.Errorf("迁移旧配置 %s 失败：%w", legacy, err)
		}
		return s, nil
	}

	if err := s.loadService(); err != nil {
		return nil, err
	}
	if err := s.loadSessions(); err != nil {
		return nil, err
	}
	s.normalize()
	return s, nil
}

// MigratedFrom 返回本次启动时被迁移的旧配置备份路径；没有迁移则为空。
func (s *Store) MigratedFrom() string { return s.migratedFrom }

// ServicePath / SessionsPath 返回两个配置文件的实际路径。
func (s *Store) ServicePath() string  { return s.servicePath }
func (s *Store) SessionsPath() string { return s.sessionsPath }

// migrateLegacy 把旧版单文件 JSON 配置拆成两个 YAML。没有旧文件时返回 false。
func (s *Store) migrateLegacy(legacy string) (bool, error) {
	data, err := os.ReadFile(legacy)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return false, nil
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return false, err
	}
	s.cfg = cfg
	s.normalize()
	if err := s.write(); err != nil {
		return false, err
	}

	backup := legacy + ".migrated"
	if _, err := os.Stat(backup); err == nil {
		backup = fmt.Sprintf("%s.migrated-%d", legacy, time.Now().Unix())
	}
	// 改名失败不致命：之后以 YAML 为准，旧文件只是留着
	if err := os.Rename(legacy, backup); err == nil {
		s.migratedFrom = backup
	} else {
		s.migratedFrom = legacy
	}
	return true, nil
}

// decodeYAML 严格解码：配置里写错了键名要报出来，而不是悄悄忽略。
func decodeYAML(data []byte, path string, out any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	return nil
}

func readFileSig(path string) ([]byte, fileSig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fileSig{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fileSig{}, err
	}
	return data, fileSig{mod: fi.ModTime(), size: fi.Size()}, nil
}

func statSig(path string) (fileSig, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileSig{}, false
	}
	return fileSig{mod: fi.ModTime(), size: fi.Size()}, true
}

// loadService 读 config.yaml 并覆盖内存里的服务部分；文件不存在则保持默认。
func (s *Store) loadService() error {
	data, sig, err := readFileSig(s.servicePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	f := serviceFile{
		Bind: s.cfg.Bind, Agent: s.cfg.Agent, Push: s.cfg.Push,
		Menu: s.cfg.Menu, Discover: s.cfg.Discover, Web: s.cfg.Web,
	}
	if err := decodeYAML(data, s.servicePath, &f); err != nil {
		return err
	}
	ignore := s.cfg.Discover.Ignore // 属于会话文件，不被服务配置覆盖
	s.cfg.Bind, s.cfg.Agent, s.cfg.Push = f.Bind, f.Agent, f.Push
	s.cfg.Menu, s.cfg.Discover, s.cfg.Web = f.Menu, f.Discover, f.Web
	s.cfg.Discover.Ignore = ignore
	s.svcData, s.svcSig = data, sig
	return nil
}

// loadSessions 读 sessions.yaml 并覆盖内存里的会话部分；文件不存在则保持默认。
func (s *Store) loadSessions() error {
	data, sig, err := readFileSig(s.sessionsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	f := sessionsFile{}
	if err := decodeYAML(data, s.sessionsPath, &f); err != nil {
		return err
	}
	if len(f.Sessions) > 0 {
		if err := validateSessions(s.sessionsPath, f.Sessions); err != nil {
			return err
		}
		s.cfg.Sessions = f.Sessions
	}
	s.cfg.Current = f.Current
	s.cfg.Discover.Ignore = f.Ignore
	s.sessData, s.sessSig = data, sig
	return nil
}

// validateSessions 校验会话里的 driver 取值，写错时拒绝启动并指出是哪个会话。
func validateSessions(path string, sessions []Session) error {
	for _, sess := range sessions {
		name := strings.TrimSpace(sess.Driver)
		if name == "" || KnownDriver(name) {
			continue
		}
		return fmt.Errorf("%s：会话 %q 的 driver=%q 不可用，可选 %s / %s",
			path, sess.ID, sess.Driver, DriverCodeBuddy, DriverCursor)
	}
	return nil
}

// refresh 发现磁盘上的文件被别的进程改过，就重新读进来。
// 不这样做的话，服务下一次写配置会把外部修改（比如刚扫码绑定的新机器人）覆盖回旧值。
func (s *Store) refresh() error {
	if sig, ok := statSig(s.servicePath); ok && sig != s.svcSig {
		if err := s.loadService(); err != nil {
			return err
		}
	}
	if sig, ok := statSig(s.sessionsPath); ok && sig != s.sessSig {
		if err := s.loadSessions(); err != nil {
			return err
		}
	}
	return nil
}

// Get 返回配置的只读快照。
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.cfg
}

// Update 修改配置并落盘。只会写入内容真正变化了的那个文件。
func (s *Store) Update(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refresh(); err != nil {
		return err
	}
	if err := fn(s.cfg); err != nil {
		return err
	}
	s.normalize()
	return s.write()
}

// SetAllowed 把 openID 加入或移出白名单。绑定者始终可用，既不会被写进名单，也不能被移出。
func (s *Store) SetAllowed(openID string, allow bool) error {
	openID = strings.TrimSpace(openID)
	return s.Update(func(c *Config) error {
		out := make([]string, 0, len(c.Bind.Whitelist)+1)
		for _, id := range c.Bind.Whitelist {
			if id == "" || id == openID {
				continue
			}
			out = append(out, id)
		}
		if allow && openID != "" && openID != c.Bind.UserOpenID {
			out = append(out, openID)
		}
		c.Bind.Whitelist = out
		return nil
	})
}

func (s *Store) normalize() {
	def := Default()
	if s.cfg.Agent.Driver == "" {
		s.cfg.Agent.Driver = def.Agent.Driver
	}
	if s.cfg.Agent.Command == "" || (s.cfg.Agent.Driver != DriverCodeBuddy && s.cfg.Agent.Command == DefaultCommand(DriverCodeBuddy)) {
		// 默认可执行文件跟着驱动走：切到 cursor 时不至于还去拉 codebuddy。
		// 第二种情况是「换驱动但没改 command」，这时 command 还是 codebuddy 的默认值，
		// 按没配置处理；用户显式写了别的路径就原样保留。
		s.cfg.Agent.Command = DefaultCommand(s.cfg.Agent.Driver)
	}
	for i := range s.cfg.Sessions {
		s.cfg.Sessions[i].Driver = strings.TrimSpace(s.cfg.Sessions[i].Driver)
	}
	if s.cfg.Agent.PermissionMode == "" {
		s.cfg.Agent.PermissionMode = def.Agent.PermissionMode
	}
	if s.cfg.Agent.TimeoutMinutes <= 0 {
		s.cfg.Agent.TimeoutMinutes = def.Agent.TimeoutMinutes
	}
	if s.cfg.Push.MaxMessages <= 0 {
		s.cfg.Push.MaxMessages = def.Push.MaxMessages
	}
	if s.cfg.Push.ImageMax <= 0 {
		s.cfg.Push.ImageMax = def.Push.ImageMax
	}
	if s.cfg.Web.Addr == "" {
		s.cfg.Web.Addr = def.Web.Addr
	}
	if s.cfg.Discover.Root == "" {
		s.cfg.Discover.Root = def.Discover.Root
	}
	if len(s.cfg.Sessions) == 0 {
		s.cfg.Sessions = def.Sessions
	}
	if s.cfg.Current == "" {
		s.cfg.Current = s.cfg.Sessions[0].ID
	}
}

// marshal 把一份 YAML 序列化成「注释头 + 缩进 2 空格」的格式。
func marshal(header string, v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(header)
	buf.WriteString("\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// write 把变化的部分原子写入，权限 0600。内容没变的文件不碰，
// 所以会话的日常变动（切换、更新 lastUsed）不会改写含 AppSecret 的 config.yaml。
func (s *Store) write() error {
	if err := os.MkdirAll(filepath.Dir(s.servicePath), 0o700); err != nil {
		return err
	}
	c := s.cfg
	svc, err := marshal(serviceHeader, serviceFile{
		Bind: c.Bind, Agent: c.Agent, Push: c.Push, Menu: c.Menu, Discover: c.Discover, Web: c.Web,
	})
	if err != nil {
		return err
	}
	sess, err := marshal(sessionsHeader, sessionsFile{
		Current: c.Current, Sessions: c.Sessions, Ignore: c.Discover.Ignore,
	})
	if err != nil {
		return err
	}

	if !bytes.Equal(svc, s.svcData) {
		if err := writeAtomic(s.servicePath, svc); err != nil {
			return err
		}
		s.svcData = svc
		s.svcSig, _ = statSig(s.servicePath)
	}
	if !bytes.Equal(sess, s.sessData) {
		if err := writeAtomic(s.sessionsPath, sess); err != nil {
			return err
		}
		s.sessData = sess
		s.sessSig, _ = statSig(s.sessionsPath)
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
