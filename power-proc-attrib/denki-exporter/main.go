package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	pmproxyURL = flag.String("pmproxy", "http://localhost:44322", "pmproxy base URL")
	listenAddr = flag.String("listen", ":9101", "address to expose metrics on")

	reInstancePrefix = regexp.MustCompile(`^\d+ name:`)
	reGuestName      = regexp.MustCompile(`-name\s+guest=([^,\s]+)`)
	rePID            = regexp.MustCompile(`^(\d+)\s`)
)

var (
	descOverall = prometheus.NewDesc(
		"denki_power_overall_watts",
		"Total system power consumption in watts.",
		nil, nil,
	)
	descConsumed = prometheus.NewDesc(
		"denki_power_proc_consumed_watts",
		"Per-process power consumption in watts.",
		[]string{"pid", "cmd", "guest"}, nil,
	)
	descPercent = prometheus.NewDesc(
		"denki_power_proc_percent",
		"Per-process share of total power consumption.",
		[]string{"pid", "cmd", "guest"}, nil,
	)
)

type collector struct{}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descOverall
	ch <- descConsumed
	ch <- descPercent
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	instNames, err := pmIndom("openmetrics.power.proc.consumed")
	if err != nil {
		log.Printf("indom error: %v", err)
		return
	}

	fr, err := pmFetch(
		"openmetrics.power.overall",
		"openmetrics.power.proc.consumed",
		"openmetrics.power.proc.consumedpercent",
	)
	if err != nil {
		log.Printf("fetch error: %v", err)
		return
	}

	for _, mv := range fr.Values {
		switch mv.Name {
		case "openmetrics.power.overall":
			for _, inst := range mv.Instances {
				ch <- prometheus.MustNewConstMetric(descOverall, prometheus.GaugeValue, inst.Value)
			}
		case "openmetrics.power.proc.consumed":
			for _, inst := range mv.Instances {
				pid, cmd, guest := parseInstance(instNames[inst.Instance])
				ch <- prometheus.MustNewConstMetric(descConsumed, prometheus.GaugeValue, inst.Value, pid, cmd, guest)
			}
		case "openmetrics.power.proc.consumedpercent":
			for _, inst := range mv.Instances {
				pid, cmd, guest := parseInstance(instNames[inst.Instance])
				ch <- prometheus.MustNewConstMetric(descPercent, prometheus.GaugeValue, inst.Value, pid, cmd, guest)
			}
		}
	}
}

// pmproxy REST API types

type fetchResponse struct {
	Values []struct {
		Name      string `json:"name"`
		Instances []struct {
			Instance int     `json:"instance"`
			Value    float64 `json:"value"`
		} `json:"instances"`
	} `json:"values"`
}

type indomResponse struct {
	Instances []struct {
		Instance int    `json:"instance"`
		Name     string `json:"name"`
	} `json:"instances"`
}

func pmFetch(names ...string) (*fetchResponse, error) {
	u := fmt.Sprintf("%s/pmapi/fetch?names=%s", *pmproxyURL, url.QueryEscape(strings.Join(names, ",")))
	body, err := httpGet(u)
	if err != nil {
		return nil, err
	}
	var fr fetchResponse
	return &fr, json.Unmarshal(body, &fr)
}

func pmIndom(metric string) (map[int]string, error) {
	u := fmt.Sprintf("%s/pmapi/indom?name=%s", *pmproxyURL, url.QueryEscape(metric))
	body, err := httpGet(u)
	if err != nil {
		return nil, err
	}
	var ir indomResponse
	if err := json.Unmarshal(body, &ir); err != nil {
		return nil, err
	}
	m := make(map[int]string, len(ir.Instances))
	for _, inst := range ir.Instances {
		m[inst.Instance] = inst.Name
	}
	return m, nil
}

func httpGet(u string) ([]byte, error) {
	resp, err := http.Get(u) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// parseInstance parses a PCP instance name of the form:
//
//	"0 name:8589 /usr/libexec/qemu-kvm -name guest=devstack-vm,debug-threads=on"
//
// into (pid, cmd, guest). For non-qemu processes guest is empty.
func parseInstance(name string) (pid, cmd, guest string) {
	name = reInstancePrefix.ReplaceAllString(name, "")

	if m := rePID.FindStringSubmatch(name); m != nil {
		pid = m[1]
	}
	if m := reGuestName.FindStringSubmatch(name); m != nil {
		guest = m[1]
	}
	if fields := strings.Fields(name); len(fields) >= 2 {
		parts := strings.Split(fields[1], "/")
		cmd = parts[len(parts)-1]
	}
	return
}

func main() {
	flag.Parse()

	prometheus.MustRegister(&collector{})
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<a href="/metrics">metrics</a>`)
	})

	log.Printf("denki-exporter listening on %s (pmproxy: %s)", *listenAddr, *pmproxyURL)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}
