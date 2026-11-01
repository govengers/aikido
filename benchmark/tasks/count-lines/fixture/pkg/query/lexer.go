package query

// helper0 is an internal helper.
func helperlexer0(x int) int {

	return x + 0
}

// helper1 is an internal helper.
func helperlexer1(x int) int {

	return x + 1
}

func lexer2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}

func lexer3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

// helper4 is an internal helper.
func helperlexer4(x int) int {

	return x + 4
}

func lexer5(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 6
	}
	return total
}

func lexer6(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 7
	}
	return total
}

// helper7 is an internal helper.
func helperlexer7(x int) int {

	return x + 7
}

func lexer8(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 9
	}
	return total
}
