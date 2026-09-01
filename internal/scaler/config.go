package scaler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/minhthong582000/scale-to-0/internal/activity"
	pb "github.com/minhthong582000/scale-to-0/pkg/externalscaler"
)

// Defaults applied when a ScaledObject omits the corresponding metadata.
const (
	DefaultIdle      = 60 * time.Second
	DefaultTargetRPS = 10.0
)

// triggerConfig is the per-ScaledObject configuration KEDA passes on every call.
type triggerConfig struct {
	workload  activity.Key
	idle      time.Duration
	targetRPS float64
}

// parseTrigger reads the trigger metadata. The workload defaults to the
// ScaledObject's own namespace and name, which is the common case.
func parseTrigger(ref *pb.ScaledObjectRef) (triggerConfig, error) {
	cfg := triggerConfig{
		workload:  activity.Key{Namespace: ref.GetNamespace(), Name: ref.GetName()},
		idle:      DefaultIdle,
		targetRPS: DefaultTargetRPS,
	}

	md := ref.GetScalerMetadata()

	if raw, ok := md["workload"]; ok {
		key, ok := activity.ParseKey(raw)
		if !ok {
			return triggerConfig{}, fmt.Errorf("workload %q is not namespace/name", raw)
		}
		cfg.workload = key
	}
	if cfg.workload.Namespace == "" || cfg.workload.Name == "" {
		return triggerConfig{}, fmt.Errorf("workload could not be determined from the ScaledObject")
	}

	if raw, ok := md["idleSeconds"]; ok {
		secs, err := strconv.ParseFloat(raw, 64)
		if err != nil || secs <= 0 {
			return triggerConfig{}, fmt.Errorf("idleSeconds %q must be a positive number", raw)
		}
		cfg.idle = time.Duration(secs * float64(time.Second))
	}

	if raw, ok := md["targetRPS"]; ok {
		rps, err := strconv.ParseFloat(raw, 64)
		if err != nil || rps <= 0 {
			return triggerConfig{}, fmt.Errorf("targetRPS %q must be a positive number", raw)
		}
		cfg.targetRPS = rps
	}

	return cfg, nil
}

// metricName is the name KEDA registers with the HPA. It must be stable for a
// given workload and valid as a Kubernetes metric name.
func (c triggerConfig) metricName() string {
	return "envoy-rps-" + sanitize(c.workload.Namespace) + "-" + sanitize(c.workload.Name)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
