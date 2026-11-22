package web

// Test helpers that read this package's own files.
//
// **In a file of their own on purpose.** A shared helper belongs where nothing
// can compile it out: if it lived in a build-tagged page test file, every other
// test that greps a template or a source file would lose its helper with it,
// which is how one deliberate exclusion becomes six accidental ones.

import ()
