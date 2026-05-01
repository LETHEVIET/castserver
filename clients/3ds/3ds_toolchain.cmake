set(CMAKE_SYSTEM_NAME Generic)
set(CMAKE_SYSTEM_PROCESSOR armv6k)

if(NOT DEFINED ENV{DEVKITARM})
    message(FATAL_ERROR "Please set DEVKITARM in your environment. export DEVKITARM=<path to>devkitARM")
endif()

if(NOT DEFINED ENV{DEVKITPRO})
    message(FATAL_ERROR "Please set DEVKITPRO in your environment. export DEVKITPRO=<path to>devkitPro")
endif()

set(DEVKITPRO $ENV{DEVKITPRO})
set(DEVKITARM $ENV{DEVKITARM})
set(CMAKE_FIND_ROOT_PATH ${DEVKITARM} ${DEVKITPRO})

set(CMAKE_C_COMPILER   "${DEVKITARM}/bin/arm-none-eabi-gcc")
set(CMAKE_CXX_COMPILER "${DEVKITARM}/bin/arm-none-eabi-g++")
set(CMAKE_ASM_COMPILER "${DEVKITARM}/bin/arm-none-eabi-gcc")
set(CMAKE_AR           "${DEVKITARM}/bin/arm-none-eabi-ar")
set(CMAKE_RANLIB       "${DEVKITARM}/bin/arm-none-eabi-ranlib")
set(CMAKE_OBJCOPY      "${DEVKITARM}/bin/arm-none-eabi-objcopy")

set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)
set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)

set(MAKEROM    "${DEVKITPRO}/tools/bin/makerom"    CACHE PATH "Path to makerom")
set(BANNERTOOL "${DEVKITPRO}/tools/bin/bannertool"  CACHE PATH "Path to bannertool")
set(DSXTOOL    "${DEVKITPRO}/tools/bin/3dsxtool"    CACHE PATH "Path to 3dsxtool")
