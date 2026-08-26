package probe

import "fmt"

// Handler is throwaway probe code so the More file table has a larger + count.
func Handler(name string) string {
	if name == "" {
		return "empty"
	}
	out := "hello " + name
	for i := 0; i < 8; i++ {
		out = fmt.Sprintf("%s-%d", out, i)
	}
	return out
}

func Format(n int) string {
	if n < 0 {
		return "neg"
	}
	if n == 0 {
		return "zero"
	}
	return fmt.Sprintf("n=%d", n)
}
