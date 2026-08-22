package log

// NewNop returns a Logger that discards everything. It is handy as a default so
// components never hold a nil Logger.
func NewNop() Logger { return nopLogger{} }

type nopLogger struct{}

func (nopLogger) Debugf(string, ...interface{}) {}
func (nopLogger) Infof(string, ...interface{})  {}
func (nopLogger) Warnf(string, ...interface{})  {}
func (nopLogger) Errorf(string, ...interface{}) {}
func (nopLogger) Fatalf(string, ...interface{}) {}
func (nopLogger) Panicf(string, ...interface{}) {}
func (n nopLogger) WithFields(Fields) Logger    { return n }
