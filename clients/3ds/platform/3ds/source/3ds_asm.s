.data
.align 4
.global memcpy_asm_4b
.global memcpy_asm
.type memcpy_asm,   "function"
.type memcpy_asm_4b, "function"

.text
memcpy_asm:
    push { r4-r11 }
    mov  r11, r0
    add  r11, r2
    sub  r11, #1
cpy_loop:
    cmp  r0, r11
    bgt  cpy_end
    ldm  r1, { r3-r10 }
    stm  r0, { r3-r10 }
    add  r1, #32
    add  r0, #32
    b    cpy_loop
cpy_end:
    pop  { r4-r11 }
    bx   lr

memcpy_asm_4b:
    ldr  r2, [r1]
    str  r2, [r0]
    bx   lr
