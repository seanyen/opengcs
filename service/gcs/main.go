package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Microsoft/opengcs/internal/kmsg"
	"github.com/Microsoft/opengcs/internal/oc"
	"github.com/Microsoft/opengcs/internal/runtime/hcsv2"
	"github.com/Microsoft/opengcs/service/gcs/bridge"
	"github.com/Microsoft/opengcs/service/gcs/runtime/runc"
	"github.com/Microsoft/opengcs/service/gcs/transport"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

func main() {
	logLevel := flag.String("loglevel", "debug", "Logging Level: debug, info, warning, error, fatal, panic.")
	kmsgLogLevel := flag.Uint("kmsgLogLevel", uint(kmsg.Warning), "Log all kmsg entries with a priority less than or equal to the supplied level.")
	logFile := flag.String("logfile", "", "Logging Target: An optional file name/path. Omit for console output.")
	logFormat := flag.String("log-format", "text", "Logging Format: text or json")
	useInOutErr := flag.Bool("use-inouterr", false, "If true use stdin/stdout for bridge communication and stderr for logging")
	v4 := flag.Bool("v4", false, "enable the v4 protocol support and v2 schema")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "\nUsage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "    %s -loglevel=debug -logfile=/run/gcs/gcs.log\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "    %s -loglevel=info -logfile=stdout\n", os.Args[0])
	}

	flag.Parse()

	// If v4 enable opencensus
	if *v4 {
		trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
		trace.RegisterExporter(&oc.LogrusExporter{})
	}

	// Use a file instead of stdout
	if *logFile != "" {
		logFileHandle, err := os.OpenFile(*logFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"path":          *logFile,
				logrus.ErrorKey: err,
			}).Fatal("failed to create log file")
		}
		logrus.SetOutput(logFileHandle)
	}

	switch *logFormat {
	case "text":
		// retain logrus's default.
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: time.RFC3339Nano, // include ns for accurate comparisons on the host
		})
	default:
		logrus.WithFields(logrus.Fields{
			"log-format": *logFormat,
		}).Fatal("unknown log-format")
	}

	level, err := logrus.ParseLevel(*logLevel)
	if err != nil {
		logrus.Fatal(err)
	}

	logrus.SetLevel(level)

	baseLogPath := "/run/gcs/c"

	logrus.Info("GCS started")

	// Continuously log /dev/kmsg
	go kmsg.ReadForever(kmsg.LogLevel(*kmsgLogLevel))

	tport := &transport.VsockTransport{}
	rtime, err := runc.NewRuntime(baseLogPath)
	if err != nil {
		logrus.WithError(err).Fatal("failed to initialize new runc runtime")
	}
	mux := bridge.NewBridgeMux()
	b := bridge.Bridge{
		Handler:  mux,
		EnableV4: *v4,
	}
	h := hcsv2.NewHost(rtime, tport)
	b.AssignHandlers(mux, h)

	var bridgeIn io.ReadCloser
	var bridgeOut io.WriteCloser
	if *useInOutErr {
		bridgeIn = os.Stdin
		bridgeOut = os.Stdout
	} else {
		const commandPort uint32 = 0x40000000
		bridgeCon, err := tport.Dial(commandPort)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"port":          commandPort,
				logrus.ErrorKey: err,
			}).Fatal("failed to dial host vsock connection")
		}
		bridgeIn = bridgeCon
		bridgeOut = bridgeCon
	}

	err = b.ListenAndServe(bridgeIn, bridgeOut)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			logrus.ErrorKey: err,
		}).Fatal("failed to serve gcs service")
	}
}
