# Writer fixtures

Every fixture but one is built from its XML inside `write_test.go`, so each test shows what it
exercises. `libreoffice-origin.docx` is the one real file: LibreOffice 24 converted a plain-text
supply agreement (three numbered clauses) to `.docx` (`soffice --headless --convert-to docx`), so
its parts, prefixes, part order, styles, font table, theme and settings are LibreOffice's, not
ours. `TestLibreOfficeOriginRoundTrip` edits it and `TestRenderedFilesOpenInLibreOffice` converts
every rendered file back with LibreOffice where it is installed.
