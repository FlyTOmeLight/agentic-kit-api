package contextbuilder

import "unicode"

const CJKWeightedCounterVersion = "cjk-weighted-v1"

type cjkWeightedCounter struct{}

func (cjkWeightedCounter) Count(text string) int64 {
	var weighted int64
	for _, r := range text {
		weighted++
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hiragana, r) ||
			(r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			weighted++
		}
	}
	return (weighted + 2) / 3
}

func ResolveCounter(version string) (Counter, error) {
	switch version {
	case "":
		return nil, nil
	case CJKWeightedCounterVersion:
		return cjkWeightedCounter{}, nil
	default:
		return nil, ErrUnsupportedCounter
	}
}
