package collector

func collector0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func collector1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

func collector2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}

func collector3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

// helper4 is an internal helper.
func helpercollector4(x int) int {

	return x + 4
}

func collector5(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 6
	}
	return total
}

// helper6 is an internal helper.
func helpercollector6(x int) int {

	return x + 6
}

func collector7(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 8
	}
	return total
}

// helper8 is an internal helper.
func helpercollector8(x int) int {

	return x + 8
}
