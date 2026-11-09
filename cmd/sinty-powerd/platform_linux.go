package main

func platform(root string) (string, func()) {
	return root, func() {}
}
