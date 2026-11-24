package discovery

import (
	"context"
	"regexp"
	"strings"
	"time"

	"lenv/internal/homebrew"
)

var (
	formulaPattern = regexp.MustCompile(`^(postgresql|redis)(@[0-9]+(\.[0-9]+)*)?$`)
	unitPattern    = regexp.MustCompile(`^(postgresql|redis(?:-server)?)(?:-[0-9]+(?:\.[0-9]+)*)?(?:@[^/]+)?\.service$`)
)

func formulaKind(name string) string {
	match := formulaPattern.FindStringSubmatch(name)
	if match == nil {
		return ""
	}
	return match[1]
}

func unitKind(name string) string {
	match := unitPattern.FindStringSubmatch(name)
	if match == nil {
		return ""
	}
	if match[1] == "redis-server" {
		return "redis"
	}
	return match[1]
}

func (s Scanner) scanBrew(ctx context.Context, result *Result) {
	brew := homebrew.Find(s.brewPrefixes)
	if brew == "" {
		return
	}
	rows, err := homebrew.List(ctx, brew, s.run)
	if err != nil {
		result.Warnings = append(result.Warnings, err.Error())
		return
	}
	for _, row := range rows {
		kind := formulaKind(row.Name)
		loaded := row.Loaded || row.LoadedFile != "" || (row.Status != "" && row.Status != "none")
		if kind == "" || (!row.Registered && row.User == "" && !loaded) {
			continue
		}
		state := row.Status
		if state == "" || state == "none" {
			state = "unknown"
		}
		scope := "user"
		if row.User == "root" {
			scope = "system"
		}
		result.Services = append(result.Services, Service{
			Kind: kind, ID: row.Name, Manager: "homebrew", Scope: scope, State: state,
		})
	}
}

func (s Scanner) scanSystemd(ctx context.Context, result *Result, scope string) {
	common := []string{"--" + scope, "--type=service", "--full", "--no-legend", "--no-pager"}
	files, fileErr := s.query(ctx, 2*time.Second, s.systemctl, append(common, "list-unit-files")...)
	running, stateErr := s.query(ctx, 2*time.Second, s.systemctl, append(common, "list-units", "--all", "--plain")...)
	if fileErr != nil && stateErr != nil {
		result.Warnings = append(result.Warnings, "systemd "+scope+" service listing unavailable")
		return
	}
	if fileErr != nil || stateErr != nil {
		result.Warnings = append(result.Warnings, "systemd "+scope+" service listing incomplete")
	}

	services := make(map[string]Service)
	add := func(data []byte, minFields int, update func(*Service, []string)) {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < minFields {
				continue
			}
			id := fields[0]
			if kind := unitKind(id); kind != "" {
				unit, ok := services[id]
				if !ok {
					unit = Service{Kind: kind, ID: id, Manager: "systemd", Scope: scope, State: "unknown"}
				}
				update(&unit, fields)
				services[id] = unit
			}
		}
	}
	if fileErr == nil {
		add(files, 2, func(unit *Service, fields []string) { unit.Registration = fields[1] })
	}
	if stateErr == nil {
		add(running, 3, func(unit *Service, fields []string) { unit.State = fields[2] })
	}
	for _, unit := range services {
		result.Services = append(result.Services, unit)
	}
}

func (s Scanner) query(ctx context.Context, timeout time.Duration, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.run(ctx, path, args...)
}
