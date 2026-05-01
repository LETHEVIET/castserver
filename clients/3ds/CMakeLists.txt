cmake_minimum_required(VERSION 3.15)
project(3DSStreaming)

set(CMAKE_CXX_STANDARD 11)

if(NOT (DEFINED ENV{DEVKITARM} AND DEFINED ENV{DEVKITPRO}
        AND CMAKE_SYSTEM_NAME STREQUAL "Generic"))
    message(FATAL_ERROR
        "Build with the 3DS toolchain:\n"
        "  cmake -DCMAKE_TOOLCHAIN_FILE=3ds_toolchain.cmake -B build_3ds .")
endif()

enable_language(ASM)

set(DEVKITPRO $ENV{DEVKITPRO})
set(DEVKITARM $ENV{DEVKITARM})

# ---- 3DS helper macros ----------------------------------------------------

macro(add_3dsx_target target)
    if(EXISTS "${DEVKITPRO}/tools/bin/3dsxtool")
        if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/romfs")
            add_custom_command(TARGET ${target} POST_BUILD
                COMMAND ${DEVKITPRO}/tools/bin/3dsxtool
                        ${target} ${target}.3dsx
                        --smdh=${target}.smdh
                        --romfs=${CMAKE_CURRENT_SOURCE_DIR}/romfs
                COMMENT "Building 3DSX with RomFS")
        else()
            add_custom_command(TARGET ${target} POST_BUILD
                COMMAND ${DEVKITPRO}/tools/bin/3dsxtool
                        ${target} ${target}.3dsx
                        --smdh=${target}.smdh
                COMMENT "Building 3DSX")
        endif()
    else()
        add_custom_command(TARGET ${target} POST_BUILD
            COMMAND ${CMAKE_COMMAND} -E copy ${target} ${target}.3dsx
            COMMENT "3dsxtool not found — placeholder 3DSX")
    endif()
endmacro()

function(process_shader shader_file output_dir)
    get_filename_component(shader_name ${shader_file} NAME_WE)
    set(shader_bin "${output_dir}/${shader_name}.shbin")
    set(shader_h   "${output_dir}/${shader_name}_shbin.h")
    set(shader_obj "${output_dir}/${shader_name}.shbin.o")
    add_custom_command(
        OUTPUT ${shader_bin} ${shader_h} ${shader_obj}
        COMMAND ${DEVKITPRO}/tools/bin/picasso ${shader_file} -o ${shader_bin}
        COMMAND ${DEVKITPRO}/tools/bin/bin2s   ${shader_bin} > ${shader_bin}.s
        COMMAND ${DEVKITARM}/bin/arm-none-eabi-gcc -c ${shader_bin}.s -o ${shader_obj}
        COMMAND ${DEVKITPRO}/tools/bin/bin2s   ${shader_bin} -H ${shader_h}
        DEPENDS ${shader_file}
        COMMENT "Compiling shader ${shader_file}"
        VERBATIM)
    set(SHADER_OBJ_FILES ${SHADER_OBJ_FILES} ${shader_obj} PARENT_SCOPE)
endfunction()

# ---- Compiler flags -------------------------------------------------------

set(ARCH_FLAGS "-march=armv6k -mtune=mpcore -mfloat-abi=hard -mtp=soft")
set(CMAKE_C_FLAGS
    "${CMAKE_C_FLAGS} -g -Wall -O2 -mword-relocations -ffunction-sections ${ARCH_FLAGS}")
set(CMAKE_CXX_FLAGS
    "${CMAKE_C_FLAGS} -fno-rtti -fno-exceptions -std=gnu++11")
set(CMAKE_ASM_FLAGS "-g ${ARCH_FLAGS}")
set(CMAKE_EXE_LINKER_FLAGS
    "-specs=3dsx.specs -g ${ARCH_FLAGS} -Wl,-Map,${PROJECT_NAME}.map")

# ---- Directories ----------------------------------------------------------

set(PLATFORM_DIR ${CMAKE_CURRENT_SOURCE_DIR}/platform/3ds/source)
set(INCLUDES_DIR ${CMAKE_CURRENT_SOURCE_DIR}/platform/3ds/include)

include_directories(
    ${INCLUDES_DIR}
    ${PLATFORM_DIR}
    ${DEVKITPRO}/libctru/include
    ${DEVKITPRO}/citro2d/include
    ${DEVKITPRO}/citro3d/include
    ${DEVKITARM}/arm-none-eabi/include
    ${DEVKITPRO}/portlibs/armv6k/include
    ${CMAKE_BINARY_DIR}
)

link_directories(
    ${DEVKITPRO}/libctru/lib
    ${DEVKITPRO}/citro2d/lib
    ${DEVKITPRO}/citro3d/lib
    ${DEVKITPRO}/portlibs/armv6k/lib
)

# ---- Sources --------------------------------------------------------------

set(SOURCES
    ${PLATFORM_DIR}/main_3ds.cpp
    ${PLATFORM_DIR}/draw.cpp
    ${PLATFORM_DIR}/gui.cpp
    ${PLATFORM_DIR}/logger.cpp
    ${PLATFORM_DIR}/3ds_asm.s
    ${PLATFORM_DIR}/net/udp_rx.cpp
    ${PLATFORM_DIR}/net/http_ctrl.cpp
    ${PLATFORM_DIR}/render/yuv_render.cpp
)

# Compile any .v.pica shaders found alongside sources (none expected in v1,
# but keep the hook so adding shaders later doesn't require CMake edits)
file(GLOB SHADER_FILES "${PLATFORM_DIR}/*.v.pica")
set(SHADER_OBJ_FILES "")
foreach(SF ${SHADER_FILES})
    process_shader(${SF} ${CMAKE_CURRENT_BINARY_DIR})
endforeach()
list(APPEND SOURCES ${SHADER_OBJ_FILES})

# ---- Executable -----------------------------------------------------------

add_executable(3ds_streaming ${SOURCES})
target_link_libraries(3ds_streaming citro2d citro3d ctru m)

# Pass CMake cache variables through as compile definitions
# (so -DMY_IP='"x.x.x.x"' actually reaches config.h)
if(DEFINED MY_IP)
    target_compile_definitions(3ds_streaming PRIVATE MY_IP=${MY_IP})
endif()
if(DEFINED SERVER_HOST)
    target_compile_definitions(3ds_streaming PRIVATE SERVER_HOST=${SERVER_HOST})
endif()
if(DEFINED SOURCE_URL)
    target_compile_definitions(3ds_streaming PRIVATE SOURCE_URL=${SOURCE_URL})
endif()

# ---- SMDH + 3DSX ----------------------------------------------------------

set(APP_TITLE       "3DS Streaming")
set(APP_DESCRIPTION "H.264 LAN video streaming")
set(APP_AUTHOR      "User")
set(APP_ICON        "${DEVKITPRO}/libctru/default_icon.png")

add_custom_command(TARGET 3ds_streaming POST_BUILD
    COMMAND ${DEVKITPRO}/tools/bin/smdhtool
            --create "${APP_TITLE}" "${APP_DESCRIPTION}" "${APP_AUTHOR}"
            "${APP_ICON}" 3ds_streaming.smdh
    COMMENT "Creating SMDH")

add_3dsx_target(3ds_streaming)
