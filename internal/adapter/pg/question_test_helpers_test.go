// question_test_helpers_test.go — small time-pointer helpers used by the
// QuestionRepository scan stubs in question_repository_test.go.
package pg_test

import "time"

// timePtr is a typed-nil alias so the scan stub's `cols` slice can express a
// "nil *time.Time" without a bare `nil` interface (the assign helper requires
// a concrete pointer type to satisfy `**time.Time` dest).
type timePtr *time.Time

func timeNowVal() time.Time { return time.Now().UTC() }
