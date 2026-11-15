package contextbuilder

const ByteEstimateVersion = "utf8-byte-estimate-v1"

type byteEstimateCounter struct{}

func (byteEstimateCounter) Count(text string) int64 {
	return int64(len(text))
}

func ResolveCounter(version string) (Counter, error) {
	switch version {
	case "":
		return nil, nil
	case ByteEstimateVersion:
		return byteEstimateCounter{}, nil
	default:
		return nil, ErrUnsupportedCounter
	}
}
