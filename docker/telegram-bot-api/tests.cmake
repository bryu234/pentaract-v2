# Included by the pinned Bot API root project during the image build.
add_executable(pentaract-endpoint-tests /build/pentaract/endpoint-tests.cpp)
target_link_libraries(pentaract-endpoint-tests PRIVATE tdcore)
