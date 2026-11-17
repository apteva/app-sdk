package sdk

// ResourcePolicy is shared by Server, execution backends and app runtimes.
// Zero values inherit OS defaults. Network settings apply to new sockets only.
import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

const ResourcePolicyVersion = "apteva.resources/v1"

type ResourcePolicy struct {
	Process          ProcessResources          `json:"process,omitempty" yaml:"process,omitempty"`
	TotalConcurrency int                       `json:"total_concurrency,omitempty" yaml:"total_concurrency,omitempty"`
	Workloads        map[string]WorkloadPolicy `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	Network          map[string]SocketPolicy   `json:"network,omitempty" yaml:"network,omitempty"`
}
type ProcessResources struct {
	CPUWeight       int   `json:"cpu_weight,omitempty" yaml:"cpu_weight,omitempty"`
	CPUQuotaPercent int   `json:"cpu_quota_percent,omitempty" yaml:"cpu_quota_percent,omitempty"`
	MemoryHighBytes int64 `json:"memory_high_bytes,omitempty" yaml:"memory_high_bytes,omitempty"`
	MemoryMaxBytes  int64 `json:"memory_max_bytes,omitempty" yaml:"memory_max_bytes,omitempty"`
	IOWeight        int   `json:"io_weight,omitempty" yaml:"io_weight,omitempty"`
	Nice            *int  `json:"nice,omitempty" yaml:"nice,omitempty"`
}
type WorkloadPolicy struct {
	Concurrency int `json:"concurrency" yaml:"concurrency"`
	Queue       int `json:"queue" yaml:"queue"`
	Weight      int `json:"weight" yaml:"weight"`
}
type SocketPolicy struct {
	ReceiveBytes int  `json:"receive_bytes,omitempty" yaml:"receive_bytes,omitempty"`
	SendBytes    int  `json:"send_bytes,omitempty" yaml:"send_bytes,omitempty"`
	DSCP         *int `json:"dscp,omitempty" yaml:"dscp,omitempty"`
}
type ResourceDeclaration struct {
	Ceilings ResourceCeilings `json:"ceilings,omitempty" yaml:"ceilings,omitempty"`
	Defaults ResourcePolicy   `json:"defaults" yaml:"defaults"`
	// Overrides cannot introduce undeclared workloads or socket classes.
	Workloads      []string `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	TrafficClasses []string `json:"traffic_classes,omitempty" yaml:"traffic_classes,omitempty"`
}
type ResourceCapabilities struct {
	Contract        string   `json:"contract"`
	ProcessControls []string `json:"process_controls"`
	ProcessApply    string   `json:"process_apply"`
	WorkloadApply   string   `json:"workload_apply"`
	NetworkApply    string   `json:"network_apply"`
	Limitations     []string `json:"limitations,omitempty"`
}

var resourceName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (p ResourcePolicy) Validate() error {
	x := p.Process
	if x.CPUWeight < 0 || x.CPUWeight > 10000 || x.IOWeight < 0 || x.IOWeight > 10000 {
		return fmt.Errorf("CPU and IO weights must be 1..10000, or zero to inherit")
	}
	if x.CPUQuotaPercent < 0 || x.CPUQuotaPercent > 6400 {
		return fmt.Errorf("CPU quota must be 1..6400 percent, or zero to inherit")
	}
	if x.MemoryHighBytes < 0 || x.MemoryMaxBytes < 0 || (x.MemoryHighBytes > 0 && x.MemoryMaxBytes > 0 && x.MemoryHighBytes > x.MemoryMaxBytes) {
		return fmt.Errorf("invalid memory high/max limits")
	}
	if x.Nice != nil && (*x.Nice < -20 || *x.Nice > 19) {
		return fmt.Errorf("nice must be -20..19")
	}
	if p.TotalConcurrency < 0 || p.TotalConcurrency > 1024 || len(p.Workloads) > 32 || len(p.Network) > 16 {
		return fmt.Errorf("resource policy exceeds bounds")
	}
	for name, w := range p.Workloads {
		if !resourceName.MatchString(name) || w.Concurrency < 1 || w.Concurrency > 1024 || w.Queue < 0 || w.Queue > 4096 || w.Weight < 1 || w.Weight > 1000 {
			return fmt.Errorf("invalid workload %q", name)
		}
	}
	for name, n := range p.Network {
		if !resourceName.MatchString(name) {
			return fmt.Errorf("invalid traffic class")
		}
		if e := n.Validate(); e != nil {
			return fmt.Errorf("%s: %w", name, e)
		}
	}
	return nil
}
func (p SocketPolicy) Validate() error {
	if p.ReceiveBytes < 0 || p.ReceiveBytes > 4<<20 || p.SendBytes < 0 || p.SendBytes > 1<<20 {
		return fmt.Errorf("socket receive/send limits are 4 MiB/1 MiB")
	}
	if p.DSCP != nil && (*p.DSCP < 0 || *p.DSCP > 63) {
		return fmt.Errorf("DSCP must be 0..63")
	}
	return nil
}
func (p ProcessResources) Empty() bool {
	return p.CPUWeight == 0 && p.CPUQuotaPercent == 0 && p.MemoryHighBytes == 0 && p.MemoryMaxBytes == 0 && p.IOWeight == 0 && p.Nice == nil
}

// SystemdProperties contains validated numeric assignments only. No shell parsing.
func (p ProcessResources) SystemdProperties() ([]string, error) {
	if e := (ResourcePolicy{Process: p}).Validate(); e != nil {
		return nil, e
	}
	out := []string{}
	for k, v := range map[string]int64{"CPUWeight": int64(p.CPUWeight), "IOWeight": int64(p.IOWeight), "MemoryHigh": p.MemoryHighBytes, "MemoryMax": p.MemoryMaxBytes} {
		if v > 0 {
			out = append(out, fmt.Sprintf("%s=%d", k, v))
		}
	}
	if p.CPUQuotaPercent > 0 {
		out = append(out, fmt.Sprintf("CPUQuota=%d%%", p.CPUQuotaPercent))
	}
	if p.Nice != nil {
		out = append(out, fmt.Sprintf("Nice=%d", *p.Nice))
	}
	sort.Strings(out)
	return out, nil
}

// ResolveResources replaces explicitly supplied workload/class entries; unset
// process fields inherit. Use an empty installation policy to reset inheritance.
func ResolveResources(d *ResourceDeclaration, overrides ...*ResourcePolicy) (ResourcePolicy, error) {
	p := ResourcePolicy{}
	if d != nil {
		p = d.Defaults
	}
	// Copy maps, avoiding mutations of manifests or previously resolved policies.
	b, _ := json.Marshal(p)
	p = ResourcePolicy{}
	_ = json.Unmarshal(b, &p)
	if p.Workloads == nil {
		p.Workloads = map[string]WorkloadPolicy{}
	}
	if p.Network == nil {
		p.Network = map[string]SocketPolicy{}
	}
	for _, o := range overrides {
		if o == nil {
			continue
		}
		if e := o.Validate(); e != nil {
			return p, e
		}
		x := o.Process
		if x.CPUWeight != 0 {
			p.Process.CPUWeight = x.CPUWeight
		}
		if x.CPUQuotaPercent != 0 {
			p.Process.CPUQuotaPercent = x.CPUQuotaPercent
		}
		if x.MemoryHighBytes != 0 {
			p.Process.MemoryHighBytes = x.MemoryHighBytes
		}
		if x.MemoryMaxBytes != 0 {
			p.Process.MemoryMaxBytes = x.MemoryMaxBytes
		}
		if x.IOWeight != 0 {
			p.Process.IOWeight = x.IOWeight
		}
		if x.Nice != nil {
			p.Process.Nice = x.Nice
		}
		if o.TotalConcurrency != 0 {
			p.TotalConcurrency = o.TotalConcurrency
		}
		for k, v := range o.Workloads {
			p.Workloads[k] = v
		}
		for k, v := range o.Network {
			p.Network[k] = v
		}
	}
	if e := p.Validate(); e != nil {
		return p, e
	}
	contains := func(a []string, k string) bool {
		for _, v := range a {
			if v == k {
				return true
			}
		}
		return false
	}
	for k := range p.Workloads {
		if d == nil || !contains(d.Workloads, k) {
			return p, fmt.Errorf("undeclared workload %q", k)
		}
	}
	for k := range p.Network {
		if d == nil || !contains(d.TrafficClasses, k) {
			return p, fmt.Errorf("undeclared traffic class %q", k)
		}
	}
	if d != nil {
		if e := d.Ceilings.Check(p); e != nil {
			return p, e
		}
	}
	return cloneResourcePolicy(p), nil
}

// ResourceCeilings further restrict the hard protocol bounds. A host may set
// these independently of app defaults; zero leaves that bound unspecified.
type ResourceCeilings struct {
	CPUWeight       int   `json:"cpu_weight,omitempty" yaml:"cpu_weight,omitempty"`
	CPUQuotaPercent int   `json:"cpu_quota_percent,omitempty" yaml:"cpu_quota_percent,omitempty"`
	Concurrency     int   `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	Queue           int   `json:"queue,omitempty" yaml:"queue,omitempty"`
	ReceiveBytes    int   `json:"receive_bytes,omitempty" yaml:"receive_bytes,omitempty"`
	SendBytes       int   `json:"send_bytes,omitempty" yaml:"send_bytes,omitempty"`
	MinimumNice     *int  `json:"minimum_nice,omitempty" yaml:"minimum_nice,omitempty"`
	AllowedDSCP     []int `json:"allowed_dscp,omitempty" yaml:"allowed_dscp,omitempty"`
}

func (c ResourceCeilings) Check(p ResourcePolicy) error {
	bound := func(name string, v, max int) error {
		if max < 0 || (max > 0 && v > max) {
			return fmt.Errorf("%s exceeds administrative/app ceiling", name)
		}
		return nil
	}
	if e := bound("cpu_weight", p.Process.CPUWeight, c.CPUWeight); e != nil {
		return e
	}
	if e := bound("cpu_quota_percent", p.Process.CPUQuotaPercent, c.CPUQuotaPercent); e != nil {
		return e
	}
	if e := bound("total_concurrency", p.TotalConcurrency, c.Concurrency); e != nil {
		return e
	}
	if c.MinimumNice != nil && p.Process.Nice != nil && *p.Process.Nice < *c.MinimumNice {
		return fmt.Errorf("nice exceeds administrative priority")
	}
	for name, w := range p.Workloads {
		if e := bound(name+" concurrency", w.Concurrency, c.Concurrency); e != nil {
			return e
		}
		if e := bound(name+" queue", w.Queue, c.Queue); e != nil {
			return e
		}
	}
	for name, n := range p.Network {
		if e := bound(name+" receive", n.ReceiveBytes, c.ReceiveBytes); e != nil {
			return e
		}
		if e := bound(name+" send", n.SendBytes, c.SendBytes); e != nil {
			return e
		}
		if n.DSCP != nil && c.AllowedDSCP != nil {
			found := false
			for _, v := range c.AllowedDSCP {
				if *n.DSCP == v {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%s DSCP not permitted", name)
			}
		}
	}
	return nil
}

func cloneResourcePolicy(p ResourcePolicy) ResourcePolicy {
	b, _ := json.Marshal(p)
	var copy ResourcePolicy
	_ = json.Unmarshal(b, &copy)
	return copy
}

func (c *ResourceCapabilities) ValidateProcess(p ProcessResources) error {
	if p.Empty() {
		return nil
	}
	if c == nil || c.Contract != ResourcePolicyVersion {
		return fmt.Errorf("backend lacks resource contract")
	}
	b, _ := json.Marshal(p)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	for name := range fields {
		found := false
		for _, v := range c.ProcessControls {
			if v == name {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("backend cannot enforce %s", name)
		}
	}
	return nil
}
