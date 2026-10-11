package service

import "errors"

func New() (Manager, error) { return nil, errors.ErrUnsupported }

func NewWithOptions(Options) (Manager, error) { return nil, errors.ErrUnsupported }
