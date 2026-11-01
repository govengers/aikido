package ast

// helper0 is an internal helper.
func helperast0(x int) int {

	return x + 0
}

func ast1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

// helper2 is an internal helper.
func helperast2(x int) int {

	return x + 2
}

// helper3 is an internal helper.
func helperast3(x int) int {

	return x + 3
}

func ast4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}
