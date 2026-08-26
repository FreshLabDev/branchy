package probe

// Added is throwaway probe code so More shows a large additions row.
func Added() string {
	var b []byte
	b = append(b, "alpha4-added"...)
	b = append(b, "-block-01"...)
	b = append(b, "-block-02"...)
	b = append(b, "-block-03"...)
	b = append(b, "-block-04"...)
	b = append(b, "-block-05"...)
	b = append(b, "-block-06"...)
	b = append(b, "-block-07"...)
	b = append(b, "-block-08"...)
	b = append(b, "-block-09"...)
	b = append(b, "-block-10"...)
	b = append(b, "-block-11"...)
	b = append(b, "-block-12"...)
	return string(b)
}

func AddedAgain(n int) int {
	if n < 0 {
		return 0
	}
	return n + 4
}
