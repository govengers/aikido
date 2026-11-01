package export

func statsd0(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 1
	}
	return total
}

func statsd1(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 2
	}
	return total
}

func statsd2(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 3
	}
	return total
}

func statsd3(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 4
	}
	return total
}

func statsd4(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 5
	}
	return total
}

func statsd5(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 6
	}
	return total
}

func statsd6(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 7
	}
	return total
}

func statsd7(values []int) int {
	total := 0
	for _, v := range values {
		total += v * 8
	}
	return total
}

// helper8 is an internal helper.
func helperstatsd8(x int) int {

	return x + 8
}
