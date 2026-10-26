// Package archtest enforces the allowed package dependency graph (A§1.3).
// All logic lives in deps_test.go; this file exists so tools that skip
// test-only packages still load the package.
package archtest
