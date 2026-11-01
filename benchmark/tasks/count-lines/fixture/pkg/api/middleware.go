package api

func middleware0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func middleware1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

func middleware2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}
