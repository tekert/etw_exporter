package etwmain

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ETWStatsCollector implements prometheus.Collector for ETW session and consumer statistics.
// It provides metrics on the health and performance of the ETW data collection pipeline itself.
type ETWStatsCollector struct {
	sessionManager *SessionManager
	eventHandler   *EventHandler
	log            *phusluadapter.SampledLogger

	// Metric Descriptors
	consumerEventsLostDesc *prometheus.Desc

	traceRTEventsLostDesc    *prometheus.Desc
	traceRTBuffersLostDesc   *prometheus.Desc
	traceParseErrorsDesc     *prometheus.Desc
	tracePropParseErrorsDesc *prometheus.Desc

	sessionBuffersInUseDesc  *prometheus.Desc
	sessionBuffersFreeDesc   *prometheus.Desc
	sessionEventsLostDesc    *prometheus.Desc
	sessionRTBuffersLostDesc *prometheus.Desc

	// Event handler metrics - events received by provider
	providerEventsReceivedDesc *prometheus.Desc
}

// NewETWStatsCollector creates a new ETW statistics collector.
func NewETWStatsCollector(sm *SessionManager, eh *EventHandler) *ETWStatsCollector {
	// Create a new logger instance with additional context for this collector.
	//statsLogger := logger.NewLoggerWithContext("etw_stats_collector")
	statsLogger := logger.NewSampledLoggerCtx("etw_stats_collector")

	return &ETWStatsCollector{
		sessionManager: sm,
		eventHandler:   eh,
		log:            statsLogger,

		consumerEventsLostDesc: prometheus.NewDesc(
			"etw_stat_consumer_events_lost_total",
			"Total number of events lost across all trace sessions, as reported by the consumer.",
			nil, nil,
		),
		traceRTEventsLostDesc: prometheus.NewDesc(
			"etw_stat_consumer_rt_events_lost_total",
			"Total number of real-time events lost for a specific trace, reported by the consumer's RT_LostEvent handler.",
			[]string{"trace"}, nil,
		),
		traceRTBuffersLostDesc: prometheus.NewDesc(
			"etw_stat_consumer_rt_buffers_lost_total",
			"Total number of real-time buffers lost for a specific trace, reported by the consumer's RT_LostEvent handler.",
			[]string{"trace"}, nil,
		),

		traceParseErrorsDesc: prometheus.NewDesc(
			"etw_stat_consumer_event_parse_errors_total",
			"Total number of events that failed to parse for a specific trace.",
			[]string{"trace"}, nil,
		),
		tracePropParseErrorsDesc: prometheus.NewDesc(
			"etw_stat_consumer_property_parse_errors_total",
			"Total number of event properties that failed to parse for a specific trace.",
			[]string{"trace"}, nil,
		),

		sessionBuffersInUseDesc: prometheus.NewDesc(
			"etw_stat_session_buffers_in_use",
			"The number of buffers allocated for the event tracing session's buffer pool",
			[]string{"trace"}, nil,
		),
		sessionBuffersFreeDesc: prometheus.NewDesc(
			"etw_stat_session_buffers_free",
			"The number of buffers that are allocated but unused in the event tracing session's buffer pool.",
			[]string{"trace"}, nil,
		),
		sessionEventsLostDesc: prometheus.NewDesc(
			"etw_stat_session_events_lost_total",
			"Total number of events that where not recorded by an ETW session (provider-side).",
			[]string{"trace"}, nil,
		),
		sessionRTBuffersLostDesc: prometheus.NewDesc(
			"etw_stat_session_realtime_buffers_lost_total",
			"The number of buffers that could not be delivered in real-time to the consumer (provider-side).",
			[]string{"trace"}, nil,
		),

		providerEventsReceivedDesc: prometheus.NewDesc(
			"etw_stat_handler_events_received_total",
			"Total number of events received from each ETW provider by the event handler.",
			[]string{"provider"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *ETWStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.consumerEventsLostDesc
	ch <- c.traceRTEventsLostDesc
	ch <- c.traceRTBuffersLostDesc
	ch <- c.traceParseErrorsDesc
	ch <- c.tracePropParseErrorsDesc
	ch <- c.sessionBuffersInUseDesc
	ch <- c.sessionBuffersFreeDesc
	ch <- c.sessionEventsLostDesc
	ch <- c.sessionRTBuffersLostDesc
	ch <- c.providerEventsReceivedDesc
}

// Collect implements prometheus.Collector.
// It is called by Prometheus on each scrape.
func (c *ETWStatsCollector) Collect(ch chan<- prometheus.Metric) {
	if !c.sessionManager.IsRunning() {
		return
	}

	consumer := c.sessionManager.consumer
	if consumer != nil {
		c.collectConsumerStats(ch, consumer)
	}

	// Collect session stats by querying the sessions directly
	c.collectSessionStats(ch, c.sessionManager.manifestSession)
	c.collectSessionStats(ch, c.sessionManager.kernelSession)

	// Collect event handler provider stats
	c.collectProviderEventStats(ch)
}

// collectConsumerStats gathers metrics from the ETW consumer and its traces.
func (c *ETWStatsCollector) collectConsumerStats(ch chan<- prometheus.Metric, consumer *etw.Consumer) {
	// Collect global consumer stats
	ch <- prometheus.MustNewConstMetric(
		c.consumerEventsLostDesc,
		prometheus.CounterValue,
		float64(consumer.LostEvents.Load()),
	)

	// Collect per-trace consumer stats
	traces := consumer.GetTraces()
	for traceName, trace := range traces {
		ch <- prometheus.MustNewConstMetric(
			c.traceRTEventsLostDesc,
			prometheus.CounterValue,
			float64(trace.RTLostEvents.Load()),
			traceName,
		)
		ch <- prometheus.MustNewConstMetric(
			c.traceRTBuffersLostDesc,
			prometheus.CounterValue,
			float64(trace.RTLostBuffer.Load()),
			traceName,
		)
		ch <- prometheus.MustNewConstMetric(
			c.traceParseErrorsDesc,
			prometheus.CounterValue,
			float64(trace.ErrorEvents.Load()),
			traceName,
		)
		ch <- prometheus.MustNewConstMetric(
			c.tracePropParseErrorsDesc,
			prometheus.CounterValue,
			float64(trace.ErrorPropsParse.Load()),
			traceName,
		)
	}
}

// collectSessionStats gathers metrics from a specific ETW session.
func (c *ETWStatsCollector) collectSessionStats(ch chan<- prometheus.Metric, session *etw.RealTimeSession) {
	if session == nil || !session.IsStarted() {
		return
	}

	prop, err := session.QueryTrace()
	if err != nil {
		c.log.Error().Err(err).Str("session", session.TraceName()).Msg("Failed to query trace session for stats")
		return
	}

	traceName := etw.FromUTF16Pointer(prop.GetTraceName())

	ch <- prometheus.MustNewConstMetric(
		c.sessionBuffersInUseDesc,
		prometheus.GaugeValue,
		float64(prop.NumberOfBuffers),
		traceName,
	)
	ch <- prometheus.MustNewConstMetric(
		c.sessionBuffersFreeDesc,
		prometheus.GaugeValue,
		float64(prop.FreeBuffers),
		traceName,
	)
	ch <- prometheus.MustNewConstMetric(
		c.sessionEventsLostDesc,
		prometheus.CounterValue,
		float64(prop.EventsLost),
		traceName,
	)
	ch <- prometheus.MustNewConstMetric(
		c.sessionRTBuffersLostDesc,
		prometheus.CounterValue,
		float64(prop.RealTimeBuffersLost),
		traceName,
	)
}

// newProviderMetric is a helper to create a provider event counter metric.
func (c *ETWStatsCollector) newProviderMetric(ch chan<- prometheus.Metric, providerName string, count uint64) {
	ch <- prometheus.MustNewConstMetric(
		c.providerEventsReceivedDesc,
		prometheus.CounterValue,
		float64(count),
		providerName,
	)
}

// collectProviderEventStats gathers metrics from the event handler showing
// how many events were received from each provider.
func (c *ETWStatsCollector) collectProviderEventStats(ch chan<- prometheus.Metric) {
	if c.eventHandler == nil {
		return
	}

	// Collect event counts for each provider type
	c.newProviderMetric(ch, "microsoft-windows-kernel-disk", c.eventHandler.GetDiskEventCount())
	c.newProviderMetric(ch, "microsoft-windows-kernel-process", c.eventHandler.GetProcessEventCount())
	c.newProviderMetric(ch, "thread-kernel", c.eventHandler.GetThreadEventCount())
	c.newProviderMetric(ch, "microsoft-windows-kernel-file", c.eventHandler.GetFileEventCount())
	c.newProviderMetric(ch, "perfinfo-kernel", c.eventHandler.GetPerfInfoEventCount())
	c.newProviderMetric(ch, "system-config", c.eventHandler.GetSystemConfigEventCount())
	c.newProviderMetric(ch, "image-kernel", c.eventHandler.GetImageEventCount())
	c.newProviderMetric(ch, "pagefault-kernel", c.eventHandler.GetPageFaultEventCount())
	c.newProviderMetric(ch, "microsoft-windows-kernel-network", c.eventHandler.GetNetworkEventCount())
	c.newProviderMetric(ch, "microsoft-windows-kernel-registry", c.eventHandler.GetRegistryEventCount())
	c.newProviderMetric(ch, "microsoft-windows-kernel-eventtracing", c.eventHandler.GetSessionWatcherEventCount())
}
