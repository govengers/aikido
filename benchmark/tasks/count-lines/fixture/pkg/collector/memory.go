package collector

// helper0 is an internal helper.
func helpermemory0(x int) int {

	return x + 0
}

// helper1 is an internal helper.
func helpermemory1(x int) int {

	return x + 1
}

func memory2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}
