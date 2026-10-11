package service

func New() (Manager, error) {
	opts, err := defaultOptions()
	if err != nil {
		return nil, err
	}
	return NewWithOptions(opts)
}

func NewWithOptions(opts Options) (Manager, error) {
	return newSystemd(opts.ConfigDir, opts.Runner)
}
