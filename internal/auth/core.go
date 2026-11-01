package auth

// Bootstrap states contain no credentials; secrets stay in the shell.
type bootstrapState uint8

const (
	bootstrapStart bootstrapState = iota
	bootstrapFile
	bootstrapCommit
	bootstrapPrint
	bootstrapComplete
	bootstrapFailed
)

type bootstrapInput uint8

const (
	databaseEmpty bootstrapInput = iota
	databasePopulated
	fileWritten
	keyCommitted
	keyPrinted
	operationFailed
)

type bootstrapCommand uint8

const (
	writeAdminFile bootstrapCommand = iota
	commitAdminKey
	printAdminKey
	removeAdminFile
)

func step(state bootstrapState, input bootstrapInput) (bootstrapState, []bootstrapCommand) {
	if input == operationFailed {
		switch state {
		case bootstrapCommit:
			return bootstrapFailed, []bootstrapCommand{removeAdminFile}
		case bootstrapStart, bootstrapFile, bootstrapPrint:
			return bootstrapFailed, nil
		case bootstrapComplete, bootstrapFailed:
			return state, nil
		}
	}
	switch state {
	case bootstrapStart:
		switch input {
		case databaseEmpty:
			return bootstrapFile, []bootstrapCommand{writeAdminFile}
		case databasePopulated:
			return bootstrapComplete, nil
		}
	case bootstrapFile:
		if input == fileWritten {
			return bootstrapCommit, []bootstrapCommand{commitAdminKey}
		}
	case bootstrapCommit:
		if input == keyCommitted {
			return bootstrapPrint, []bootstrapCommand{printAdminKey}
		}
	case bootstrapPrint:
		if input == keyPrinted {
			return bootstrapComplete, nil
		}
	case bootstrapComplete, bootstrapFailed:
	}
	return state, nil
}

func run(inputs []bootstrapInput) bootstrapState {
	state := bootstrapStart
	for _, input := range inputs {
		state, _ = step(state, input)
	}
	return state
}
