package panel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Commands are fixed templates. Only a validated hexadecimal container ID is appended.
const metricsCommand = `LC_ALL=C; export LC_ALL
printf '%s\n' "$SSH_CONNECTION" | awk 'NF==4 {print "connectionIP=" $3}'
read _ a b c d e f g h rest < /proc/stat
t1=$((a+b+c+d+e+f+g+h)); i1=$((d+e))
sleep 1
read _ a b c d e f g h rest < /proc/stat
t2=$((a+b+c+d+e+f+g+h)); i2=$((d+e))
awk -v t="$((t2-t1))" -v i="$((i2-i1))" 'BEGIN { if(t<=0) exit 1; printf "cpu=%.2f\n",100*(t-i)/t }'
awk '/MemTotal:/ {t=$2} /MemAvailable:/ {a=$2} END {if(t<=0) exit 1; printf "memory=%.2f\nmemoryTotal=%.0f\nmemoryUsed=%.0f\n",100*(t-a)/t,t*1024,(t-a)*1024}' /proc/meminfo
df -Pk / | awk 'NR==2 {gsub(/%/,"",$5); printf "disk=%s\ndiskTotal=%.0f\ndiskUsed=%.0f\n",$5,$2*1024,$3*1024}'
awk '/^cpu[0-9]+ / {n++} END {print "cores=" n}' /proc/stat
awk '{print "uptime=" $1}' /proc/uptime
awk -F '[: ]+' 'NR>2 && $2!="lo" {r+=$3; t+=$11} END {printf "rx=%.0f\ntx=%.0f\n",r,t}' /proc/net/dev`

type cappedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - len(b.data)
	if len(p) > remaining {
		b.truncated = true
	}
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func runSSH(parent context.Context, s Server, command string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	cmd := sshCommand(ctx, s, command)
	out := &cappedBuffer{limit: 1024 * 1024}
	stderr := &cappedBuffer{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("La conexión SSH superó el tiempo de espera")
		}
		detail := strings.TrimSpace(string(stderr.data))
		if detail == "" {
			detail = "SSH no está disponible o la conexión falló"
		}
		return "", errors.New(detail)
	}
	if out.truncated {
		return "", errors.New("La respuesta SSH supera 1 MiB; no se usarán datos incompletos")
	}
	return string(out.data), nil
}

func sshCommand(ctx context.Context, s Server, command string) *exec.Cmd {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-p", strconv.Itoa(s.Port), "-l", s.User}
	if s.Identity != "" {
		args = append(args, "-i", s.Identity, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "--", s.Host, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = sshEnvironment()
	cmd.WaitDelay = time.Second
	return cmd
}

// The panel's encryption key is local-only. Do not let OpenSSH (including a
// user's SendEnv/ProxyCommand configuration) inherit it into child processes.
func sshEnvironment() []string {
	values := os.Environ()
	out := make([]string, 0, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		if !strings.EqualFold(key, "NODO_MASTER_KEY") {
			out = append(out, value)
		}
	}
	return out
}

// Streaming commands have operation-scoped deadlines. Raw Docker output may
// contain environment secrets, so it is never included in API errors or logs.
func streamSSH(ctx context.Context, s Server, command string, in io.Reader, out io.Writer) error {
	cmd := sshCommand(ctx, s, command)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("Falló el paso remoto; revisa el estado indicado antes de reintentar")
	}
	return nil
}
func parseMetrics(out string) (Sample, error) {
	values := map[string]float64{}
	var connectionIP string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k == "connectionIP" {
			if ip, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil && !ip.IsUnspecified() {
				connectionIP = ip.Unmap().String()
			}
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return Sample{}, errors.New("Métricas Linux inválidas")
		}
		values[k] = n
	}
	for _, k := range []string{"cpu", "memory", "disk", "uptime", "rx", "tx"} {
		n, ok := values[k]
		if !ok || n < 0 || n != n || n > 1e30 {
			return Sample{}, fmt.Errorf("Métrica ausente o inválida: %s", k)
		}
	}
	for _, k := range []string{"cpu", "memory", "disk"} {
		if values[k] > 100 {
			return Sample{}, errors.New("Porcentaje fuera de rango")
		}
	}
	for _, k := range []string{"cores", "memoryTotal", "memoryUsed", "diskTotal", "diskUsed"} {
		if n, ok := values[k]; ok && (n < 0 || n != n || n > 1e30) {
			return Sample{}, fmt.Errorf("Métrica inválida: %s", k)
		}
	}
	return Sample{At: time.Now().UTC().Format(time.RFC3339Nano), ConnectionIP: connectionIP, CPU: values["cpu"], Memory: values["memory"], Disk: values["disk"], Uptime: values["uptime"], RX: values["rx"], TX: values["tx"], Cores: values["cores"], MemoryTotal: values["memoryTotal"], MemoryUsed: values["memoryUsed"], DiskTotal: values["diskTotal"], DiskUsed: values["diskUsed"]}, nil
}
