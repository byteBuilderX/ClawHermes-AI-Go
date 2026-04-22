package observability

type Meter interface {
	RecordInt64(name string, value int64)
	RecordFloat64(name string, value float64)
}

type defaultMeter struct{}

func (m *defaultMeter) RecordInt64(name string, value int64) {
	// TODO: Implement metric recording
}

func (m *defaultMeter) RecordFloat64(name string, value float64) {
	// TODO: Implement metric recording
}

func GetMeter() Meter {
	return &defaultMeter{}
}
