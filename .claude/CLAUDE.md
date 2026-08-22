# Project Instructions

## GoLang Coding Standards

- write .md files into a subfolder ./docs/
- when writing unit tests, use t.Logf() to log program input and output
- never remove ToDo comments unless fully implemented
- write private functions at the end of the file after public functions
- use helper functions/packages and interfaces to avoid duplicate code. When adding similar features in a 2nd file, check if refactoring makes sense
- when returning new `MsgCode` strings on errors, ensure translations in `locales` folder are updated
- use `uint64` as Primary Keys and put GORM default time columns at the end of the table 

