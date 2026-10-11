package service

func New() (Manager, error) {
	opts, err := defaultOptions()
	if err != nil {
		return nil, err
	}
	return NewWithOptions(opts)
}

func NewWithOptions(opts Options) (Manager, error) {
	return newLaunchd(opts.HomeDir, opts.UID, opts.Runner)
}
