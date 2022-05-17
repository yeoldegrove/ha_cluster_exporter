package main

import (
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/ClusterLabs/ha_cluster_exporter/collector"
	"github.com/ClusterLabs/ha_cluster_exporter/collector/corosync"
	"github.com/ClusterLabs/ha_cluster_exporter/collector/drbd"
	"github.com/ClusterLabs/ha_cluster_exporter/collector/pacemaker"
	"github.com/ClusterLabs/ha_cluster_exporter/collector/sbd"
	"github.com/ClusterLabs/ha_cluster_exporter/internal"
)

var (
	// the released version
	version string
	// the time the binary was built
	buildDate string
	// global --help flag
	helpFlag *bool
	// global --version flag
	versionFlag *bool

	config *viper.Viper
)

func init() {
	config = viper.New()
	config.SetConfigName("ha_cluster_exporter")
	config.AddConfigPath("./")
	config.AddConfigPath("$HOME/.config/")
	config.AddConfigPath("/etc/")
	config.AddConfigPath("/usr/etc/")

	flag.String("port", "9664", "The port number to listen on for HTTP requests")
	flag.String("address", "0.0.0.0", "The address to listen on for HTTP requests")
	flag.String("log.level", "info", "The minimum logging level; levels are, in ascending order: debug, info, warn, error")
	flag.String("log-level", "info", "The minimum logging level; levels are, in ascending order: debug, info, warn, error")
	flag.String("crm-mon-path", "/usr/sbin/crm_mon", "path to crm_mon executable")
	flag.String("cibadmin-path", "/usr/sbin/cibadmin", "path to cibadmin executable")
	flag.String("corosync-cfgtoolpath-path", "/usr/sbin/corosync-cfgtool", "path to corosync-cfgtool executable")
	flag.String("corosync-quorumtool-path", "/usr/sbin/corosync-quorumtool", "path to corosync-quorumtool executable")
	flag.String("sbd-path", "/usr/sbin/sbd", "path to sbd executable")
	flag.String("sbd-config-path", "/etc/sysconfig/sbd", "path to sbd configuration")
	flag.String("drbdsetup-path", "/sbin/drbdsetup", "path to drbdsetup executable")
	flag.String("drbdsplitbrain-path", "/var/run/drbd/splitbrain", "path to drbd splitbrain hooks temporary files")
	flag.Bool("enable-timestamps", false, "Add the timestamp to every metric line")
	flag.CommandLine.MarkDeprecated("log-level", "please use --log.level")
	flag.CommandLine.MarkDeprecated("enable-timestamps", "server-side metric timestamping is discouraged by Prometheus best-practices and should be avoided")
	flag.CommandLine.SortFlags = false

	helpFlag = flag.BoolP("help", "h", false, "show this help message")
	versionFlag = flag.Bool("version", false, "show version and build information")
}

func main() {
	flag.Parse()

	switch {
	case *helpFlag:
		showHelp()
	case *versionFlag:
		showVersion()
		os.Exit(0)
	default:
		run()
	}
}

func run() {
	promlogConfig := &promlog.Config{}
	logger := promlog.New(promlogConfig)

	showVersion()

	var err error
	
	err = config.BindPFlags(flag.CommandLine)
	if err != nil {
		level.Error(logger).Log("msg", "Could not bind config to CLI flags", "err", err)
	}

	err = config.ReadInConfig()
	if err != nil {
		level.Warn(logger).Log("msg", "Reading config file failed", "err", err)
		level.Info(logger).Log("msg", "Default config values will be used")
	} else {
		level.Info(logger).Log("msg", "Using config file: " + config.ConfigFileUsed())
	}

	collectors, errors := registerCollectors(config)
	for _, err = range errors {
		level.Warn(logger).Log("msg", "Registration failure: ", "err", err)
	}
	if len(collectors) == 0 {
		level.Error(logger).Log("msg", "No collector could be registered.", "err", err)
		os.Exit(1)
	}
	for _, c := range collectors {
		if c, ok := c.(collector.SubsystemCollector); ok == true {
			level.Info(logger).Log("msg", c.GetSubsystem() + " collector registered.")
		}
	}

	// if we're not in debug log level, we unregister the Go runtime metrics collector that gets registered by default
	if config.GetString("log-level") != "debug" && config.GetString("log.level") != "debug" {
		prometheus.Unregister(prometheus.NewGoCollector())
	}

	fullListenAddress := fmt.Sprintf("%s:%s", config.Get("address"), config.Get("port"))

	http.HandleFunc("/", internal.Landing)
	http.Handle("/metrics", promhttp.Handler())

	level.Info(logger).Log("msg", "Serving metrics on " + fullListenAddress + servePath)
	listen = web.ListenAndServe(serveAddress, "", logger)
	if err := listen; err != nil {
		level.Error(logger).Log("msg", "Error starting HTTP server", "err", err)
		os.Exit(1)
	}
}

func registerCollectors(config *viper.Viper) (collectors []prometheus.Collector, errors []error) {
	pacemakerCollector, err := pacemaker.NewCollector(
		config.GetString("crm-mon-path"),
		config.GetString("cibadmin-path"),
	)
	if err != nil {
		errors = append(errors, err)
	} else {
		collectors = append(collectors, pacemakerCollector)
	}

	corosyncCollector, err := corosync.NewCollector(
		config.GetString("corosync-cfgtoolpath-path"),
		config.GetString("corosync-quorumtool-path"),
	)
	if err != nil {
		errors = append(errors, err)
	} else {
		collectors = append(collectors, corosyncCollector)
	}

	sbdCollector, err := sbd.NewCollector(
		config.GetString("sbd-path"),
		config.GetString("sbd-config-path"),
	)
	if err != nil {
		errors = append(errors, err)
	} else {
		collectors = append(collectors, sbdCollector)
	}

	drbdCollector, err := drbd.NewCollector(
		config.GetString("drbdsetup-path"),
		config.GetString("drbdsplitbrain-path"),
	)
	if err != nil {
		errors = append(errors, err)
	} else {
		collectors = append(collectors, drbdCollector)
	}

	for i, c := range collectors {
		if c, ok := c.(collector.InstrumentableCollector); ok == true {
			collectors[i] = collector.NewInstrumentedCollector(c)
		}
	}

	prometheus.MustRegister(collectors...)

	return collectors, errors
}

func showHelp() {
	flag.Usage()
	os.Exit(0)
}

func showVersion() {
	if buildDate == "" {
		buildDate = "at unknown time"
	}
	fmt.Printf("version %s\nbuilt with %s %s/%s %s\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH, buildDate)
}
