package knowledge

import "fmt"

// The name curate approves and retires under
// No caller may write it so the records tell what a person decided from what curate carried out
const ApproverNodloop = "nodloop"

// The name a person decides under
// 1. an empty name fails with ErrApproverRequired
// 2. the reserved name fails with ErrApproverReserved
func CheckApprover(name string) error {
	if name == "" {
		return ErrApproverRequired
	}
	return refuseReserved(name)
}

// Every write a person decides refuses the reserved name from any caller
// before it reads a record so no argument, flag, key or config value can write it
func refuseReserved(name string) error {
	if name == ApproverNodloop {
		return fmt.Errorf("%w: %s", ErrApproverReserved, name)
	}
	return nil
}
