package commandstation

func xorSum(b []byte) byte {
	var x byte
	for _, v := range b {
		x ^= v
	}
	return x
}
