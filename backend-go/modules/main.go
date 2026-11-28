package main

import (
	"fmt"
	"sync"
)

func main() {
	var wg sync.WaitGroup
	wg.Add(2)

	wg.Go(func() {
		m1("lxr", &wg)
	})

	wg.Go(func() {
		defer wg.Done()
		fmt.Println("1234")
		fmt.Println("5678")
		fmt.Println("4321")
	})

	wg.Wait()

}

func m1(name string, wg *sync.WaitGroup) {
	defer wg.Done()
	fmt.Println("Hello " + name)
}
