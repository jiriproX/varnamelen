package warningsreceiver

type foo struct{}

func (f *foo) Receiver() { // want `method receiver name 'f' is too short for the scope of its usage`
	// fill
	// fill
	// fill
	// fill
	// fill
	_ = f
}
