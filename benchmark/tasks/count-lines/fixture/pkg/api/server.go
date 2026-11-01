package api

// helper0 is an internal helper.
func helperserver0(x int) int {

	return x + 0
}

// helper1 is an internal helper.
func helperserver1(x int) int {

	return x + 1
}

// helper2 is an internal helper.
func helperserver2(x int) int {

	return x + 2
}

func server3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

func server4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}

// helper5 is an internal helper.
func helperserver5(x int) int {

	return x + 5
}

func server6(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 7
	}
	return total
}

func server7(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 8
	}
	return total
}

func server8(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 9
	}
	return total
}

func server9(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 10
	}
	return total
}

func server10(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 11
	}
	return total
}
