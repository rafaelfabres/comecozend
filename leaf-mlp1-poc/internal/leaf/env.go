package leaf

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultPlatform      = "mlp1"
	defaultPrimaryRoot   = "/mnt/sdcard"
	defaultSecondaryRoot = "/media/sdcard1"
	AppStateName         = "Itch-io"
)

type getenvFunc func(string) (string, bool)

// Environment is the Leaf runtime-path contract resolved for this process.
// A sourced env.sh is already reflected in the process environment; explicit
// values therefore win before the direct-launch fallbacks below are applied.
type Environment struct {
	Platform         string
	Device           string
	SDCardPath       string
	SystemPath       string
	PlatformPath     string
	UserdataPath     string
	LogsPath         string
	RuntimePath      string
	InternalDataPath string
	Sources          SourceList
}

func LoadEnvironment() (Environment, error) {
	return loadEnvironment(os.LookupEnv)
}

func loadEnvironment(getenv getenvFunc) (Environment, error) {
	value := func(name, fallback string) string {
		if v, ok := getenv(name); ok && v != "" {
			return v
		}
		return fallback
	}

	platform := value("PLATFORM", DefaultPlatform)
	if platform != DefaultPlatform {
		return Environment{}, fmt.Errorf("unsupported Leaf platform %q", platform)
	}
	primary := cleanPath(value("SDCARD_PATH", defaultPrimaryRoot))
	secondary := cleanPath(value("UMRK_SECONDARY_SDCARD_PATH", defaultSecondaryRoot))

	roots, err := resolveRootList(getenv, primary, secondary)
	if err != nil {
		return Environment{}, err
	}
	sources, err := resolveSources(getenv, roots, secondary)
	if err != nil {
		return Environment{}, err
	}

	systemPath := cleanPath(value("SYSTEM_PATH",
		filepath.Join(primary, ".system", "leaf", "platforms", platform)))
	userdataPath := cleanPath(value("USERDATA_PATH",
		filepath.Join(primary, ".userdata", platform)))

	return Environment{
		Platform:         platform,
		Device:           value("DEVICE", platform),
		SDCardPath:       primary,
		SystemPath:       systemPath,
		PlatformPath:     cleanPath(value("UMRK_PLATFORM_PATH", systemPath)),
		UserdataPath:     userdataPath,
		LogsPath:         cleanPath(value("LOGS_PATH", filepath.Join(userdataPath, "logs"))),
		RuntimePath:      cleanPath(value("UMRK_RUNTIME_PATH", filepath.Join(os.TempDir(), "jawaka-runtime"))),
		InternalDataPath: cleanPath(value("UMRK_INTERNAL_DATA_PATH", filepath.Join(primary, ".umrk", platform))),
		Sources:          sources,
	}, nil
}

func (e Environment) StateDir() string {
	return filepath.Join(e.UserdataPath, AppStateName)
}

func (e Environment) CatalogPath() string {
	return filepath.Join(e.PlatformPath, "defaults", "systems.json")
}

// EnsureAppDirs creates only app-owned durable state and the shared log root.
func (e Environment) EnsureAppDirs() error {
	for _, dir := range []string{e.StateDir(), e.LogsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create Leaf app directory %s: %w", dir, err)
		}
	}
	return nil
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
