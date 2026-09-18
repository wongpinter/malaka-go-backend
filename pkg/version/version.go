package version

import (
	"fmt"
	"runtime"
)

var (
	Version = "DEV-0.15.0"

	GitCommit = "dev"

	BuildDate = "2026-09-08"
)

type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Compiler  string `json:"compiler"`
	Platform  string `json:"platform"`
}

func Get() Info {
	return Info{
		Version:   Version,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Compiler:  runtime.Compiler,
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

func String() string {
	return fmt.Sprintf("%s (%s, %s)", Version, GitCommit, BuildDate)
}
