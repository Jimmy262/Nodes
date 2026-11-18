package panel

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Choose a service only when the definition is unambiguous. Database services
// with expose-only ports do not displace the sole published web service.
func githubDefaultService(z *zipProject) string {
	if len(z.services) == 1 {
		return z.services[0]
	}
	candidates := []string{}
	for _, name := range z.services {
		service := z.config["services"].(map[string]any)[name].(map[string]any)
		if ports, ok := service["ports"].([]any); ok && len(ports) > 0 {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}

func githubDefaultPort(z *zipProject, service string) int {
	dockerfile := path.Join(z.root, "Dockerfile")
	if !z.dockerfile {
		definition, ok := z.config["services"].(map[string]any)[service].(map[string]any)
		if !ok {
			return 0
		}
		for _, key := range []string{"ports", "expose"} {
			ports := map[int]bool{}
			unknown := false
			values, _ := definition[key].([]any)
			for _, value := range values {
				if item, ok := value.(map[string]any); ok {
					if strings.EqualFold(fmt.Sprint(item["protocol"]), "udp") {
						continue
					}
					value = item["target"]
				}
				text := strings.ToLower(fmt.Sprint(value))
				if strings.HasSuffix(text, "/udp") {
					continue
				}
				parts := strings.Split(strings.Split(text, "/")[0], ":")
				n, err := strconv.Atoi(parts[len(parts)-1])
				if err == nil && n > 0 && n <= 65535 {
					ports[n] = true
				} else {
					unknown = true
				}
			}
			if len(ports) > 1 || unknown {
				return 0
			}
			for port := range ports {
				return port
			}
		}
		context, filename := ".", "Dockerfile"
		switch build := definition["build"].(type) {
		case string:
			context = build
		case map[string]any:
			if build["target"] != nil || build["dockerfile_inline"] != nil {
				return 0
			}
			if value, ok := build["context"].(string); ok {
				context = value
			}
			if value, ok := build["dockerfile"].(string); ok {
				filename = value
			}
		default:
			return 0
		}
		if !relativeProjectPath(context) || !relativeProjectPath(filename) {
			return 0
		}
		dockerfile = path.Join(z.root, context, filename)
	}
	// EXPOSE in a builder stage does not describe the final runtime image.
	from := regexp.MustCompile(`(?i)^FROM\s+`)
	expose := regexp.MustCompile(`(?i)^EXPOSE\s+(.+)$`)
	for _, file := range z.files {
		if file.Name != dockerfile {
			continue
		}
		raw, err := zipRead(file)
		if err != nil {
			return 0
		}
		ports := map[int]bool{}
		unknown := false
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if from.MatchString(line) {
				ports = map[int]bool{}
				unknown = false
			}
			match := expose.FindStringSubmatch(line)
			if len(match) != 2 {
				continue
			}
			for _, value := range strings.Fields(strings.SplitN(match[1], "#", 2)[0]) {
				if strings.HasSuffix(strings.ToLower(value), "/udp") {
					continue
				}
				n, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(value), "/tcp"))
				if err == nil && n > 0 && n <= 65535 {
					ports[n] = true
				} else {
					unknown = true
				}
			}
		}
		if len(ports) == 1 && !unknown {
			for port := range ports {
				return port
			}
		}
	}
	return 0
}
