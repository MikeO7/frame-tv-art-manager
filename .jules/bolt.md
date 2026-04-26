## 2024-05-15 - [Batch Disk I/O Operations]
**Learning:** In Go applications, performing disk I/O operations (like saving JSON mappings) inside a loop can severely degrade performance, especially when uploading multiple items or running in environments with slower disks.
**Action:** Always extract disk I/O operations, such as saving state files or mappings, outside of loops to batch them and improve overall performance by reducing repeated filesystem access. This is particularly important for functions that iterate over collections to perform tasks like uploads or downloads.
