package probe

func Handler(name string) string {
	if name == "" {
		return "alpha4"
	}
	return "hello " + name
}
