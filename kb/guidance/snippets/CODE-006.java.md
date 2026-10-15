Arguments left out of the marker:

```java
LocalActivityOptions options =
    LocalActivityOptions.newBuilder()
        .setStartToCloseTimeout(Duration.ofSeconds(5))
        .setDoNotIncludeArgumentsIntoMarker(true)
        .build();
```
